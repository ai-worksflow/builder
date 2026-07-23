package qualificationinputauthority

import (
	"bytes"
	"context"
	"errors"
	"fmt"
)

type sourceVerifierAdapter struct {
	binding ExecutableBinding
	verify  SourceVerificationFunc
}

type credentialResolverAdapter struct {
	binding ExecutableBinding
	resolve CredentialResolutionFunc
}

func NewSourceVerifier(binding ExecutableBinding, verify SourceVerificationFunc) (SourceVerifier, error) {
	if err := validateExecutableBinding("sourceVerifier", binding); err != nil {
		return nil, err
	}
	if verify == nil {
		return nil, invalid("sourceVerifier", "trusted verification function is required")
	}
	return &sourceVerifierAdapter{binding: binding, verify: verify}, nil
}

func NewCredentialResolver(binding ExecutableBinding, resolve CredentialResolutionFunc) (CredentialResolver, error) {
	if err := validateExecutableBinding("credentialResolver", binding); err != nil {
		return nil, err
	}
	if resolve == nil {
		return nil, invalid("credentialResolver", "trusted resolution function is required")
	}
	return &credentialResolverAdapter{binding: binding, resolve: resolve}, nil
}

func (adapter *sourceVerifierAdapter) sourceBinding() ExecutableBinding {
	if adapter == nil {
		return ExecutableBinding{}
	}
	return adapter.binding
}

func (adapter *credentialResolverAdapter) credentialBinding() ExecutableBinding {
	if adapter == nil {
		return ExecutableBinding{}
	}
	return adapter.binding
}

func (adapter *sourceVerifierAdapter) verifySource(
	ctx context.Context,
	request SourceVerificationRequest,
	requestBytes []byte,
	requestHash string,
) (verifiedSourceGrant, error) {
	if adapter == nil || adapter.verify == nil || ctx == nil {
		return verifiedSourceGrant{}, invalid("sourceVerifier", "adapter, function, and context are required")
	}
	exactBytes, exactHash, err := EncodeSourceRequest(request)
	if err != nil || !bytes.Equal(exactBytes, requestBytes) || exactHash != requestHash {
		return verifiedSourceGrant{}, invalid("sourceVerifier", "request document, bytes, or hash differ")
	}
	if request.Verifier != adapter.binding {
		return verifiedSourceGrant{}, invalid("sourceVerifier", "request does not bind this reviewed adapter identity and executable digest")
	}
	observation, err := adapter.verify(ctx, request, append([]byte(nil), requestBytes...), requestHash)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return verifiedSourceGrant{}, err
		}
		return verifiedSourceGrant{}, fmt.Errorf("%w: source verifier rejected the exact request", ErrNotReady)
	}
	if observation.RequestHash != requestHash || !validDigest(observation.ReceiptHash) ||
		observation.ReceiptHash == requestHash || observation.ReceiptHash == adapter.binding.ExecutableDigest {
		return verifiedSourceGrant{}, invalid("sourceVerifier.observation", "does not bind a distinct immutable receipt to the exact request")
	}
	return verifiedSourceGrant{
		proof: VerificationProof{
			AuthorityID:      adapter.binding.AuthorityID,
			ExecutableDigest: adapter.binding.ExecutableDigest,
			ReceiptHash:      observation.ReceiptHash,
			RequestHash:      requestHash,
		},
		requestBytes: append([]byte(nil), requestBytes...),
	}, nil
}

func (adapter *credentialResolverAdapter) resolveCredential(
	ctx context.Context,
	request CredentialResolutionRequest,
	requestBytes []byte,
	requestHash string,
) (verifiedCredentialGrant, error) {
	if adapter == nil || adapter.resolve == nil || ctx == nil {
		return verifiedCredentialGrant{}, invalid("credentialResolver", "adapter, function, and context are required")
	}
	exactBytes, exactHash, err := EncodeCredentialRequest(request)
	if err != nil || !bytes.Equal(exactBytes, requestBytes) || exactHash != requestHash {
		return verifiedCredentialGrant{}, invalid("credentialResolver", "request document, bytes, or hash differ")
	}
	if request.Resolver != adapter.binding {
		return verifiedCredentialGrant{}, invalid("credentialResolver", "request does not bind this reviewed adapter identity and executable digest")
	}
	observation, err := adapter.resolve(ctx, request, append([]byte(nil), requestBytes...), requestHash)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return verifiedCredentialGrant{}, err
		}
		return verifiedCredentialGrant{}, fmt.Errorf("%w: credential resolver rejected the exact request", ErrNotReady)
	}
	if observation.RequestHash != requestHash || !validDigest(observation.ReceiptHash) ||
		observation.ReceiptHash == requestHash || observation.ReceiptHash == adapter.binding.ExecutableDigest {
		return verifiedCredentialGrant{}, invalid("credentialResolver.observation", "does not bind a distinct immutable receipt to the exact request")
	}
	return verifiedCredentialGrant{
		proof: VerificationProof{
			AuthorityID:      adapter.binding.AuthorityID,
			ExecutableDigest: adapter.binding.ExecutableDigest,
			ReceiptHash:      observation.ReceiptHash,
			RequestHash:      requestHash,
		},
		requestBytes: append([]byte(nil), requestBytes...),
	}, nil
}
