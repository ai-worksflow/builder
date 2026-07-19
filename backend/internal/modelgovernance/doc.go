// Package modelgovernance defines the immutable, offline contracts used to
// describe a candidate model/runtime combination and its frozen conformance
// corpus.
//
// Parsing or validating a document is not an approval or activation decision.
// This package also defines the independent signed governance chain and its
// append-only activation and signed Genesis service boundaries, but production
// wiring, authoritative disable-state integration, corpus/shadow execution,
// production-signed Genesis materials and provider execution remain external.
// In particular, a ProviderBinding RouteID is an opaque registry identity, not
// a URL and never authority to make a network request. Its RouteAuthorityHash
// must be resolved against a separately verified provider-route authority and
// an egress policy.
package modelgovernance
