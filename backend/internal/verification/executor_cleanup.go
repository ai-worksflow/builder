package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var verificationContainerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

const verificationNetworkListFormat = "{{.ID}}\t{{.Name}}"

// CleanupVerificationEnvironment removes only resources carrying the exact
// Attempt and fence labels. Attempt-wide legacy resources (currently the
// network name) are removed only while the materializer's cross-process fence
// lock proves this generation still owns them.
func (executor *DockerCandidateExecutor) CleanupVerificationEnvironment(
	ctx context.Context,
	input VerificationEnvironmentCleanup,
) error {
	if executor == nil || ctx == nil || validateVerificationExecutionFence(input.Fence) != nil {
		return fmt.Errorf("%w: invalid exact execution cleanup", ErrCandidateExecution)
	}
	environment := verificationContainerEnvironment(executor.root, executor.runtimeName, executor.daemonHost)
	filter := []string{
		"ps", "-aq",
		"--filter", "label=worksflow.verification.attempt=" + input.Fence.AttemptID,
		"--filter", "label=worksflow.verification.fence=" + strconv.FormatUint(input.Fence.AttemptFenceEpoch, 10),
	}
	var cleanupErr error
	containerIDs, err := executor.listVerificationContainerIDs(ctx, filter, environment)
	if err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("list exact-fence containers: %w", err))
	} else {
		if len(containerIDs) > 0 {
			if err := executor.commands.Run(
				ctx, executor.runtimePath, append([]string{"rm", "-f"}, containerIDs...),
				environment, io.Discard, io.Discard,
			); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove exact-fence containers: %w", err))
			}
		}
	}

	executor.mu.Lock()
	runtimeState, found := executor.runtimes[input.Fence.AttemptID]
	if found && runtimeState.Fence == input.Fence.AttemptFenceEpoch {
		delete(executor.runtimes, input.Fence.AttemptID)
	}
	executor.mu.Unlock()

	networkFilter := []string{
		"--filter", "label=worksflow.verification.attempt=" + input.Fence.AttemptID,
		"--filter", "label=worksflow.verification.fence=" + strconv.FormatUint(input.Fence.AttemptFenceEpoch, 10),
	}
	networkIDs, networkListErr := executor.listVerificationNetworkIDs(
		ctx, networkFilter, environment, verificationNetworkName(input.Fence.AttemptID),
	)
	if networkListErr != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("list exact-fence networks: %w", networkListErr))
	} else if len(networkIDs) > 0 {
		if err := executor.commands.Run(
			ctx, executor.runtimePath, append([]string{"network", "rm"}, networkIDs...),
			environment, io.Discard, io.Discard,
		); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove exact-fence networks: %w", err))
		}
	} else if input.OwnsSharedRuntime {
		// Releases created before network labels were introduced are cleaned only
		// while the materializer lock and runtime marker prove exact ownership.
		// Listing first makes an absent legacy network idempotent without hiding a
		// daemon outage or a failed removal.
		name := verificationNetworkName(input.Fence.AttemptID)
		legacyFilter := []string{"--filter", "name=^" + regexp.QuoteMeta(name) + "$"}
		legacyIDs, err := executor.listVerificationNetworkIDs(ctx, legacyFilter, environment, name)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("list legacy Attempt network: %w", err))
		} else if len(legacyIDs) > 0 {
			if err := executor.commands.Run(
				ctx, executor.runtimePath, append([]string{"network", "rm"}, legacyIDs...),
				environment, io.Discard, io.Discard,
			); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove legacy Attempt network: %w", err))
			}
		}
	}
	if cleanupErr != nil {
		return fmt.Errorf("%w: %v", ErrCandidateExecution, cleanupErr)
	}
	return nil
}

func (executor *DockerCandidateExecutor) listVerificationContainerIDs(
	ctx context.Context,
	args []string,
	environment []string,
) ([]string, error) {
	var identities bytes.Buffer
	if err := executor.commands.Run(
		ctx, executor.runtimePath, args, environment, &identities, io.Discard,
	); err != nil {
		return nil, err
	}
	result := strings.Fields(identities.String())
	for _, identity := range result {
		if !verificationContainerIDPattern.MatchString(identity) {
			return nil, errors.New("runtime returned an invalid resource identity")
		}
	}
	return result, nil
}

// Docker prints network IDs for `network ls -q`, while Podman prints names.
// An explicit format gives both runtimes the same identity projection. The
// exact generated name is checked in addition to the hexadecimal ID before an
// ID is ever passed to network rm.
func (executor *DockerCandidateExecutor) listVerificationNetworkIDs(
	ctx context.Context,
	filters []string,
	environment []string,
	expectedName string,
) ([]string, error) {
	var identities bytes.Buffer
	args := []string{"network", "ls", "--format", verificationNetworkListFormat}
	args = append(args, filters...)
	if err := executor.commands.Run(
		ctx, executor.runtimePath, args, environment, &identities, io.Discard,
	); err != nil {
		return nil, err
	}
	output := strings.TrimSpace(identities.String())
	if output == "" {
		return nil, nil
	}
	result := make([]string, 0, strings.Count(output, "\n")+1)
	seen := make(map[string]struct{})
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			return nil, errors.New("runtime returned a malformed network identity")
		}
		identity, name := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if !verificationContainerIDPattern.MatchString(identity) || name != expectedName {
			return nil, errors.New("runtime returned an invalid network identity")
		}
		if _, duplicate := seen[identity]; duplicate {
			return nil, errors.New("runtime returned a duplicate network identity")
		}
		seen[identity] = struct{}{}
		result = append(result, identity)
	}
	return result, nil
}
