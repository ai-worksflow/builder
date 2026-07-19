package productionpostgres

import (
	"crypto/x509"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const maximumCredentialFileBytes = 16 * 1024

const maximumTrustAnchorFileBytes = 1024 * 1024

var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

type validatedDSN struct {
	scoped          string
	username        string
	password        string
	host            string
	port            string
	database        string
	rootCertificate string
}

type validatedConfig struct {
	schema        string
	application   validatedDSN
	migrator      validatedDSN
	qualification validatedDSN
	promotion     validatedDSN
}

// ReadCredentialFile reads one DSN from an absolute, non-symlinked,
// single-link file owned by the current effective user with mode 0400 or 0600.
// One trailing LF is accepted for secret-manager compatibility.
func ReadCredentialFile(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) ||
		strings.ContainsAny(path, "\r\n\x00") {
		return "", errors.New("credential path must be absolute and normalized")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", errors.New("credential path must not contain symlinks")
	}

	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("credential file cannot be opened securely")
	}
	file := os.NewFile(uintptr(descriptor), "postgres-credential")
	if file == nil {
		_ = unix.Close(descriptor)
		return "", errors.New("credential file descriptor is unavailable")
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil || !validCredentialFileInfo(before) {
		return "", errors.New("credential file must be a bounded, private regular file")
	}
	pathBefore, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, pathBefore) || !validCredentialFileInfo(pathBefore) {
		return "", errors.New("credential file identity changed while opening")
	}

	encoded, err := io.ReadAll(io.LimitReader(file, maximumCredentialFileBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximumCredentialFileBytes {
		return "", errors.New("credential file is empty, unreadable, or too large")
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(after, pathAfter) ||
		!validCredentialFileInfo(after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("credential file changed while reading")
	}

	value := string(encoded)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("credential file must contain one canonical DSN line")
	}
	return value, nil
}

func validCredentialFileInfo(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumCredentialFileBytes {
		return false
	}
	permissions := info.Mode().Perm()
	if permissions != 0o400 && permissions != 0o600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && int(stat.Uid) == os.Geteuid()
}

// VerifyTrustAnchorFile pins the CA file identity before any connection is
// opened. Trust anchors are public, so root-owned 0444 files are accepted, but
// neither symlink/hardlink replacement nor group/other writes are allowed.
func VerifyTrustAnchorFile(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) ||
		strings.ContainsAny(path, "\r\n\x00") {
		return errors.New("TLS trust anchor path must be absolute and normalized")
	}
	if err := verifyTrustAnchorAncestors(path); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return errors.New("TLS trust anchor path must not contain symlinks")
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("TLS trust anchor cannot be opened securely")
	}
	file := os.NewFile(uintptr(descriptor), "postgres-trust-anchor")
	if file == nil {
		_ = unix.Close(descriptor)
		return errors.New("TLS trust anchor descriptor is unavailable")
	}
	defer file.Close()

	before, err := file.Stat()
	pathBefore, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, pathBefore) ||
		!validTrustAnchorFileInfo(before) || !validTrustAnchorFileInfo(pathBefore) {
		return errors.New("TLS trust anchor is not one stable private-integrity regular file")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maximumTrustAnchorFileBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maximumTrustAnchorFileBytes {
		return errors.New("TLS trust anchor is empty, unreadable, or too large")
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(after, pathAfter) ||
		!validTrustAnchorFileInfo(after) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return errors.New("TLS trust anchor changed while reading")
	}
	if err := verifyTrustAnchorAncestors(path); err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(encoded) {
		return errors.New("TLS trust anchor contains no parseable certificate")
	}
	return nil
}

func verifyTrustAnchorAncestors(path string) error {
	directory := filepath.Dir(path)
	for {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm()&0o022 != 0 {
			return errors.New("TLS trust anchor ancestors must be non-writable trusted directories")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && int(stat.Uid) != os.Geteuid()) {
			return errors.New("TLS trust anchor ancestor ownership is untrusted")
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil
		}
		directory = parent
	}
}

func validTrustAnchorFileInfo(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumTrustAnchorFileBytes ||
		info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o111 != 0 || info.Mode().Perm()&0o400 == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && (stat.Uid == 0 || int(stat.Uid) == os.Geteuid())
}

func validateConfig(config Config) (validatedConfig, error) {
	if !validSchema(config.Schema) {
		return validatedConfig{}, errors.New("trusted schema is invalid")
	}
	application, err := validateDSN(config.ApplicationDSN, config.Schema)
	if err != nil {
		return validatedConfig{}, errors.New("application DSN is invalid")
	}
	migrator, err := validateDSN(config.MigratorDSN, config.Schema)
	if err != nil {
		return validatedConfig{}, errors.New("migrator DSN is invalid")
	}
	qualification, err := validateDSN(config.QualificationDSN, config.Schema)
	if err != nil {
		return validatedConfig{}, errors.New("qualification DSN is invalid")
	}
	promotion, err := validateDSN(config.PromotionDSN, config.Schema)
	if err != nil {
		return validatedConfig{}, errors.New("promotion DSN is invalid")
	}
	all := []validatedDSN{application, migrator, qualification, promotion}
	for left := 0; left < len(all); left++ {
		for right := left + 1; right < len(all); right++ {
			if all[left].username == all[right].username {
				return validatedConfig{}, errors.New("PostgreSQL login identities must be distinct")
			}
			if all[left].password == all[right].password {
				return validatedConfig{}, errors.New("PostgreSQL credential secrets must be distinct")
			}
			if all[left].host != all[right].host || all[left].port != all[right].port ||
				all[left].database != all[right].database {
				return validatedConfig{}, errors.New("PostgreSQL credentials must target one database endpoint")
			}
			if all[left].rootCertificate != all[right].rootCertificate {
				return validatedConfig{}, errors.New("PostgreSQL credentials must use one TLS trust policy")
			}
		}
	}
	return validatedConfig{
		schema:        config.Schema,
		application:   application,
		migrator:      migrator,
		qualification: qualification,
		promotion:     promotion,
	}, nil
}

func validateDSN(raw, schema string) (validatedDSN, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") {
		return validatedDSN{}, errors.New("DSN must be one canonical URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.RawPath != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.Hostname() != strings.ToLower(parsed.Hostname()) || parsed.User == nil ||
		parsed.User.Username() == "" || strings.Trim(parsed.Path, "/") == "" || parsed.Path == "/" ||
		parsed.String() != raw {
		return validatedDSN{}, errors.New("DSN URL shape is invalid")
	}
	password, hasPassword := parsed.User.Password()
	if !hasPassword || password == "" || strings.ContainsAny(password, "\r\n\x00") {
		return validatedDSN{}, errors.New("DSN must carry a non-empty credential secret")
	}
	username := parsed.User.Username()
	if len(username) > 63 || username != strings.TrimSpace(username) || strings.ContainsAny(username, "\r\n\x00") {
		return validatedDSN{}, errors.New("DSN login identity is invalid")
	}
	database, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil || database == "" || strings.Contains(database, "/") || len(database) > 63 ||
		database != strings.TrimSpace(database) || strings.ContainsAny(database, "\r\n\x00") {
		return validatedDSN{}, errors.New("DSN database name is invalid")
	}
	port := parsed.Port()
	if port == "" {
		port = "5432"
	} else {
		numeric, parseErr := strconv.Atoi(port)
		if parseErr != nil || numeric < 1 || numeric > 65535 {
			return validatedDSN{}, errors.New("DSN port is invalid")
		}
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || query.Encode() != parsed.RawQuery {
		return validatedDSN{}, errors.New("DSN query is not canonical")
	}
	rootCertificate := ""
	verifiedTLS := false
	primaryReadWrite := false
	for key, values := range query {
		if len(values) != 1 {
			return validatedDSN{}, errors.New("DSN query contains duplicate values")
		}
		value := values[0]
		switch key {
		case "sslmode":
			if value != "verify-full" {
				return validatedDSN{}, errors.New("DSN must verify the PostgreSQL server identity")
			}
			verifiedTLS = true
		case "sslrootcert":
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return validatedDSN{}, errors.New("DSN TLS root certificate path is invalid")
			}
			rootCertificate = value
		case "sslcert", "sslkey":
			return validatedDSN{}, errors.New("DSN client TLS credentials are unsupported; use the isolated password secret")
		case "connect_timeout":
			seconds, parseErr := strconv.Atoi(value)
			if parseErr != nil || seconds < 1 || seconds > 300 {
				return validatedDSN{}, errors.New("DSN connect timeout is invalid")
			}
		case "target_session_attrs":
			if value != "read-write" {
				return validatedDSN{}, errors.New("DSN must target a read-write primary")
			}
			primaryReadWrite = true
		case "application_name":
			if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
				return validatedDSN{}, errors.New("DSN application name is invalid")
			}
		default:
			return validatedDSN{}, errors.New("DSN query contains an unsupported identity or session override")
		}
	}
	if !verifiedTLS || rootCertificate == "" || !primaryReadWrite {
		return validatedDSN{}, errors.New("DSN must pin verify-full TLS, a root certificate, and a read-write primary")
	}
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return validatedDSN{
		scoped:          parsed.String(),
		username:        username,
		password:        password,
		host:            parsed.Hostname(),
		port:            port,
		database:        database,
		rootCertificate: rootCertificate,
	}, nil
}

func validSchema(value string) bool {
	return schemaPattern.MatchString(value) && !strings.HasPrefix(value, "pg_") && value != "information_schema"
}
