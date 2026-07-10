package designimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	eventAttributePattern = regexp.MustCompile(`(?i)\son[a-z0-9_-]+\s*=`)
	hrefAttributePattern  = regexp.MustCompile(`(?i)(?:href|xlink:href)\s*=\s*["']\s*([^"'\s>]+)`)
)

const (
	maxCatalogItems      = 256
	maxCatalogTraversal  = 4096
	maxFigmaCatalogDepth = 64
	maxCatalogTextRunes  = 240
)

var sourceLabels = map[SourceKind]string{
	SourceFigma: "Figma", SourcePenpot: "Penpot", SourceExcalidraw: "Excalidraw",
	SourceTLDraw: "tldraw", SourceStorybook: "Storybook", SourceLadle: "Ladle", SourceUpload: "File upload",
}

var mediaExtensions = map[string][]string{
	"application/json": {".json", ".excalidraw"},
	"image/svg+xml":    {".svg"},
	"image/png":        {".png"},
	"image/jpeg":       {".jpg", ".jpeg"},
	"image/webp":       {".webp"},
	"application/pdf":  {".pdf"},
}

var acceptedMediaBySource = map[SourceKind]map[string]bool{
	SourceFigma:      mediaSet("application/json", "image/svg+xml", "image/png", "image/jpeg", "image/webp", "application/pdf"),
	SourcePenpot:     mediaSet("application/json", "image/svg+xml", "image/png", "image/jpeg", "image/webp", "application/pdf"),
	SourceExcalidraw: mediaSet("application/json", "image/svg+xml", "image/png"),
	SourceTLDraw:     mediaSet("application/json", "image/svg+xml", "image/png"),
	SourceStorybook:  mediaSet("application/json"),
	SourceLadle:      mediaSet("application/json"),
	SourceUpload:     mediaSet("application/json", "image/svg+xml", "image/png", "image/jpeg", "image/webp", "application/pdf"),
}

func mediaSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func SupportedCapabilities() Capabilities {
	return supportedCapabilities(MaxUploadBytes)
}

func supportedCapabilities(maxUploadBytes int64) Capabilities {
	if maxUploadBytes < 0 {
		maxUploadBytes = 0
	}
	order := []SourceKind{SourceFigma, SourcePenpot, SourceExcalidraw, SourceTLDraw, SourceStorybook, SourceLadle, SourceUpload}
	sources := make([]SourceCapability, 0, len(order))
	for _, kind := range order {
		mediaTypes := make([]string, 0, len(acceptedMediaBySource[kind]))
		extensions := map[string]struct{}{}
		for mediaType := range acceptedMediaBySource[kind] {
			mediaTypes = append(mediaTypes, mediaType)
			for _, suffix := range extensionsForSource(kind, mediaType) {
				extensions[suffix] = struct{}{}
			}
		}
		sort.Strings(mediaTypes)
		suffixes := make([]string, 0, len(extensions))
		for suffix := range extensions {
			suffixes = append(suffixes, suffix)
		}
		sort.Strings(suffixes)
		uploadReason := ""
		if maxUploadBytes == 0 {
			uploadReason = "Snapshot content storage is configured below the safe envelope reserve. Increase CONTENT_MAX_BYTES to enable uploads."
		}
		sources = append(sources, SourceCapability{
			SourceKind: kind, Label: sourceLabels[kind], UploadEnabled: maxUploadBytes > 0, RemoteEnabled: false,
			UploadReason:  uploadReason,
			RemoteReason:  "No server-side connector credential is configured. Export from the source tool and upload the file instead.",
			AcceptedMedia: mediaTypes, AcceptedSuffixes: suffixes, MaxUploadBytes: maxUploadBytes,
		})
	}
	return Capabilities{
		SnapshotPolicy: "Every accepted upload is frozen as an immutable, content-addressed snapshot before a proposal is created.",
		TrustPolicy:    "External design sources are untrusted assets, not project facts; only an approved internal Prototype can flow downstream.",
		Sources:        sources,
	}
}

type validatedUpload struct {
	SourceName     string
	FileName       string
	MediaType      string
	Raw            []byte
	RawContentHash string
	Catalog        ImportCatalog
}

func validateCreateInput(input CreateInput) error {
	if _, ok := sourceLabels[input.SourceKind]; !ok {
		return invalid("sourceKind", "must identify a supported design source")
	}
	input.Mode = strings.TrimSpace(input.Mode)
	switch input.Mode {
	case "upload":
		if input.File == nil || strings.TrimSpace(input.SourceURL) != "" {
			return invalid("mode", "upload mode requires file and forbids sourceUrl")
		}
	case "remote_url":
		if input.File != nil || strings.TrimSpace(input.SourceURL) == "" {
			return invalid("mode", "remote_url mode requires sourceUrl and forbids file")
		}
		if _, err := validateRemoteURL(input.SourceURL); err != nil {
			return err
		}
		return &Error{Kind: ErrCapabilityUnavailable, Field: "sourceUrl", Detail: "remote connectors are not configured; upload an exported file"}
	default:
		return invalid("mode", "must be upload or remote_url")
	}
	if strings.TrimSpace(input.PageSpecRevision.ArtifactID) == "" || strings.TrimSpace(input.PageSpecRevision.RevisionID) == "" || !strings.HasPrefix(input.PageSpecRevision.ContentHash, "sha256:") || input.PageSpecRevision.AnchorID != nil {
		return invalid("pageSpecRevision", "must pin one whole PageSpec artifact revision and content hash")
	}
	if len(input.Title) > 240 {
		return invalid("title", "must be at most 240 characters")
	}
	if len(input.SelectedFrameIDs) > 200 {
		return invalid("selectedFrameIds", "must contain at most 200 frame identifiers")
	}
	seen := map[string]bool{}
	for index, value := range input.SelectedFrameIDs {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 240 || seen[value] {
			return invalid(fmt.Sprintf("selectedFrameIds[%d]", index), "must be a unique non-empty identifier of at most 240 characters")
		}
		seen[value] = true
	}
	return nil
}

func validateRemoteURL(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", invalid("sourceUrl", "must be at most 2048 characters")
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return "", invalid("sourceUrl", "must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return "", invalid("sourceUrl", "embedded credentials are forbidden")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", invalid("sourceUrl", "only the default HTTPS port is allowed")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return "", invalid("sourceUrl", "host must be a public DNS name")
	}
	if address, parseErr := netip.ParseAddr(host); parseErr == nil && !address.IsGlobalUnicast() || parseErr == nil && (address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast()) {
		return "", invalid("sourceUrl", "private, loopback, and link-local addresses are forbidden")
	}
	if net.ParseIP(host) != nil {
		return "", invalid("sourceUrl", "literal IP addresses are forbidden")
	}
	for key := range parsed.Query() {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key") || strings.Contains(lower, "auth") || strings.Contains(lower, "password") {
			return "", invalid("sourceUrl", "credential-like query parameters are forbidden")
		}
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func validateUpload(kind SourceKind, file UploadFile) (validatedUpload, error) {
	return validateUploadWithLimit(kind, file, MaxUploadBytes)
}

func validateUploadWithLimit(kind SourceKind, file UploadFile, maxUploadBytes int64) (validatedUpload, error) {
	if maxUploadBytes <= 0 {
		return validatedUpload{}, &Error{Kind: ErrCapabilityUnavailable, Field: "file", Detail: "snapshot storage is too small for design uploads"}
	}
	name := strings.TrimSpace(file.Name)
	if name == "" || len(name) > 240 || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return validatedUpload{}, invalid("file.name", "must be a safe base filename of at most 240 characters")
	}
	if !utf8.ValidString(name) {
		return validatedUpload{}, invalid("file.name", "must be valid UTF-8")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(file.MediaType))
	if err != nil {
		return validatedUpload{}, mediaError("file.mediaType", "must be a valid media type")
	}
	mediaType = strings.ToLower(mediaType)
	if !acceptedMediaBySource[kind][mediaType] {
		return validatedUpload{}, mediaError("file.mediaType", "is not accepted for this source")
	}
	suffix := strings.ToLower(filepath.Ext(name))
	if !contains(extensionsForSource(kind, mediaType), suffix) {
		return validatedUpload{}, mediaError("file.name", "extension does not match mediaType")
	}
	if int64(base64.StdEncoding.DecodedLen(len(file.ContentBase64))) > maxUploadBytes+2 {
		return validatedUpload{}, &Error{Kind: ErrUploadTooLarge, Field: "file.contentBase64", Detail: fmt.Sprintf("decoded content must not exceed %d bytes", maxUploadBytes)}
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(file.ContentBase64)
	if err != nil {
		return validatedUpload{}, invalid("file.contentBase64", "must be strict standard base64")
	}
	if len(raw) == 0 {
		return validatedUpload{}, invalid("file.contentBase64", "must not be empty")
	}
	if int64(len(raw)) > maxUploadBytes {
		return validatedUpload{}, &Error{Kind: ErrUploadTooLarge, Field: "file.contentBase64", Detail: fmt.Sprintf("decoded content must not exceed %d bytes", maxUploadBytes)}
	}
	if err := validateContent(kind, mediaType, raw); err != nil {
		return validatedUpload{}, err
	}
	digest := sha256.Sum256(raw)
	return validatedUpload{
		SourceName: sourceLabels[kind], FileName: name, MediaType: mediaType, Raw: raw,
		RawContentHash: "sha256:" + hex.EncodeToString(digest[:]), Catalog: extractCatalog(kind, mediaType, raw),
	}, nil
}

func extensionsForSource(kind SourceKind, mediaType string) []string {
	if mediaType == "application/json" && kind != SourceExcalidraw && kind != SourceUpload {
		return []string{".json"}
	}
	return mediaExtensions[mediaType]
}

func validateContent(kind SourceKind, mediaType string, raw []byte) error {
	switch mediaType {
	case "application/json":
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return mediaError("file.contentBase64", "declared JSON is malformed")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return mediaError("file.contentBase64", "JSON must contain exactly one value")
		}
		object, isObject := value.(map[string]any)
		if !isObject {
			return mediaError("file.contentBase64", "design exports and manifests must be JSON objects")
		}
		switch kind {
		case SourceFigma:
			if _, document := object["document"].(map[string]any); !document {
				if _, nodes := object["nodes"].(map[string]any); !nodes {
					return mediaError("file.contentBase64", "Figma JSON must contain a document or nodes export")
				}
			}
		case SourcePenpot:
			_, pages := object["pages"]
			_, components := object["components"]
			_, objects := object["objects"]
			if !pages && !components && !objects && !strings.EqualFold(stringValue(object["type"]), "penpot") {
				return mediaError("file.contentBase64", "Penpot JSON must contain pages, components, objects, or type=penpot")
			}
		case SourceExcalidraw:
			if _, ok := object["elements"]; !ok && !strings.EqualFold(stringValue(object["type"]), "excalidraw") {
				return mediaError("file.contentBase64", "Excalidraw JSON must contain elements or type=excalidraw")
			}
		case SourceTLDraw:
			if _, store := object["store"]; !store {
				if _, records := object["records"]; !records {
					return mediaError("file.contentBase64", "tldraw JSON must contain store or records")
				}
			}
		case SourceStorybook, SourceLadle:
			if _, entries := object["entries"]; !entries {
				if _, stories := object["stories"]; !stories {
					return mediaError("file.contentBase64", "component manifest must contain entries or stories")
				}
			}
		}
	case "image/svg+xml":
		if !utf8.Valid(raw) {
			return mediaError("file.contentBase64", "SVG must be valid UTF-8")
		}
		lower := strings.ToLower(string(raw))
		unsafeReference := false
		for _, match := range hrefAttributePattern.FindAllSubmatch(raw, -1) {
			if len(match) > 1 && !bytes.HasPrefix(bytes.TrimSpace(match[1]), []byte("#")) {
				unsafeReference = true
				break
			}
		}
		if !strings.Contains(lower, "<svg") || strings.Contains(lower, "<script") || strings.Contains(lower, "<foreignobject") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<!entity") || strings.Contains(lower, "@import") || strings.Contains(lower, "url(") || eventAttributePattern.Match(raw) || unsafeReference || strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:text/html") {
			return mediaError("file.contentBase64", "SVG contains active or external content and was rejected")
		}
	case "image/png":
		if len(raw) < 8 || !bytes.Equal(raw[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
			return mediaError("file.contentBase64", "content does not have a PNG signature")
		}
	case "image/jpeg":
		if len(raw) < 4 || raw[0] != 0xff || raw[1] != 0xd8 || raw[len(raw)-2] != 0xff || raw[len(raw)-1] != 0xd9 {
			return mediaError("file.contentBase64", "content does not have a complete JPEG signature")
		}
	case "image/webp":
		if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
			return mediaError("file.contentBase64", "content does not have a WebP signature")
		}
	case "application/pdf":
		if len(raw) < 8 || !bytes.HasPrefix(raw, []byte("%PDF-")) || !bytes.Contains(raw[max(0, len(raw)-2048):], []byte("%%EOF")) {
			return mediaError("file.contentBase64", "content does not have a complete PDF signature")
		}
		lower := bytes.ToLower(raw)
		if bytes.Contains(lower, []byte("/javascript")) || bytes.Contains(lower, []byte("/openaction")) || bytes.Contains(lower, []byte("/launch")) {
			return mediaError("file.contentBase64", "PDF contains active actions and was rejected")
		}
	default:
		return mediaError("file.mediaType", "is not supported")
	}
	return nil
}

func extractCatalog(kind SourceKind, mediaType string, raw []byte) ImportCatalog {
	catalog := ImportCatalog{Pages: []CatalogItem{}, Components: []CatalogItem{}, States: []CatalogItem{}, Interactions: []CatalogItem{}}
	if mediaType != "application/json" {
		catalog.Pages = append(catalog.Pages, CatalogItem{ID: "asset-1", Name: "Imported asset", Kind: strings.TrimPrefix(mediaType, "image/")})
		return catalog
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return catalog
	}
	switch kind {
	case SourceFigma:
		if document, ok := object["document"].(map[string]any); ok {
			extractFigmaNodes(document, &catalog)
		} else {
			for _, node := range mapValues(object["nodes"]) {
				extractFigmaNodes(node, &catalog)
				if catalog.Truncated {
					break
				}
			}
		}
	case SourceExcalidraw:
		if elements, ok := object["elements"].([]any); ok {
			catalog.Pages = append(catalog.Pages, CatalogItem{ID: "canvas", Name: "Excalidraw canvas", Kind: "canvas", Count: len(elements)})
			for index, element := range elements {
				if node, ok := element.(map[string]any); ok && stringValue(node["type"]) == "frame" {
					catalog.Pages = append(catalog.Pages, catalogFromMap(node, index, "frame"))
				}
			}
		}
	case SourceTLDraw:
		records := mapValues(object["store"])
		if len(records) == 0 {
			records = sliceValues(object["records"])
		}
		for index, record := range records {
			typeName := stringValue(record["typeName"])
			if typeName == "page" || strings.Contains(stringValue(record["type"]), "frame") {
				catalog.Pages = append(catalog.Pages, catalogFromMap(record, index, typeName))
			}
		}
	case SourceStorybook, SourceLadle:
		entries := mapValues(object["entries"])
		if len(entries) == 0 {
			entries = mapValues(object["stories"])
		}
		for index, entry := range entries {
			item := catalogFromMap(entry, index, "story")
			catalog.Components = append(catalog.Components, item)
			catalog.States = append(catalog.States, CatalogItem{ID: item.ID, Name: item.Name, Kind: "story"})
		}
	default:
		if pages := sliceValues(object["pages"]); len(pages) > 0 {
			for index, page := range pages {
				catalog.Pages = append(catalog.Pages, catalogFromMap(page, index, "page"))
			}
		}
		if components := sliceValues(object["components"]); len(components) > 0 {
			for index, component := range components {
				catalog.Components = append(catalog.Components, catalogFromMap(component, index, "component"))
			}
		}
	}
	if len(catalog.Pages) == 0 {
		catalog.Pages = append(catalog.Pages, CatalogItem{ID: "document", Name: sourceLabels[kind] + " document", Kind: "document"})
	}
	return normalizeCatalog(catalog)
}

func extractFigmaNodes(node map[string]any, catalog *ImportCatalog) {
	type queuedNode struct {
		value map[string]any
		depth int
	}
	queue := []queuedNode{{value: node}}
	visited := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		visited++
		if visited > maxCatalogTraversal {
			catalog.Truncated = true
			catalog.TruncationReason = "Figma node traversal exceeded the safe limit"
			return
		}
		typeName := strings.ToUpper(stringValue(current.value["type"]))
		item := CatalogItem{
			ID:   fallback(stringValue(current.value["id"]), fmt.Sprintf("node-%d", visited)),
			Name: fallback(stringValue(current.value["name"]), typeName), Kind: strings.ToLower(typeName),
		}
		switch typeName {
		case "CANVAS", "FRAME", "SECTION":
			catalog.Pages = append(catalog.Pages, item)
		case "COMPONENT", "COMPONENT_SET", "INSTANCE":
			catalog.Components = append(catalog.Components, item)
		}
		children := sliceValues(current.value["children"])
		if current.depth >= maxFigmaCatalogDepth {
			if len(children) > 0 {
				catalog.Truncated = true
				catalog.TruncationReason = "Figma node depth exceeded the safe limit"
			}
			continue
		}
		for _, child := range children {
			queue = append(queue, queuedNode{value: child, depth: current.depth + 1})
		}
	}
}

func catalogFromMap(value map[string]any, index int, kind string) CatalogItem {
	id := fallback(stringValue(value["id"]), fallback(stringValue(value["key"]), fmt.Sprintf("%s-%d", kind, index+1)))
	name := fallback(stringValue(value["name"]), fallback(stringValue(value["title"]), id))
	return CatalogItem{ID: truncateCatalogText(id), Name: truncateCatalogText(name), Kind: truncateCatalogText(fallback(stringValue(value["type"]), kind))}
}

func normalizeCatalog(catalog ImportCatalog) ImportCatalog {
	remaining := maxCatalogItems
	normalize := func(items []CatalogItem) []CatalogItem {
		if remaining <= 0 {
			if len(items) > 0 {
				catalog.Truncated = true
			}
			return []CatalogItem{}
		}
		limit := len(items)
		if limit > remaining {
			limit = remaining
			catalog.Truncated = true
		}
		result := make([]CatalogItem, 0, limit)
		for _, item := range items[:limit] {
			item.ID = truncateCatalogText(item.ID)
			item.Name = truncateCatalogText(item.Name)
			item.Kind = truncateCatalogText(item.Kind)
			result = append(result, item)
		}
		remaining -= limit
		return result
	}
	catalog.Pages = normalize(catalog.Pages)
	catalog.Components = normalize(catalog.Components)
	catalog.States = normalize(catalog.States)
	catalog.Interactions = normalize(catalog.Interactions)
	if catalog.Truncated && catalog.TruncationReason == "" {
		catalog.TruncationReason = "Catalog item count exceeded the safe limit"
	}
	return catalog
}

func truncateCatalogText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxCatalogTextRunes {
		runes = runes[:maxCatalogTextRunes]
	}
	return string(runes)
}

func mapValues(value any) []map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	limit := len(keys)
	if limit > maxCatalogTraversal {
		limit = maxCatalogTraversal
	}
	result := make([]map[string]any, 0, limit)
	for _, key := range keys[:limit] {
		if item, ok := object[key].(map[string]any); ok {
			if _, exists := item["id"]; !exists {
				copyItem := make(map[string]any, len(item)+1)
				for itemKey, itemValue := range item {
					copyItem[itemKey] = itemValue
				}
				copyItem["id"] = key
				item = copyItem
			}
			result = append(result, item)
		}
	}
	return result
}

func sliceValues(value any) []map[string]any {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if object, ok := value.(map[string]any); ok {
			result = append(result, object)
		}
	}
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

func fallback(value, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
