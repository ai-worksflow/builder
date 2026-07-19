package app

import (
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/config"
	"github.com/worksflow/builder/backend/internal/repository"
)

func TestRepositorySearchAdmissionOptionsPreserveEveryConfiguredBoundary(t *testing.T) {
	configured := config.RepositorySearchAdmissionConfig{
		RedisPrefix: "worksflow:test:repository-search:",
		Timeout:     73 * time.Millisecond,
		QueryProject: config.RepositorySearchRateBucketConfig{
			RefillTokens: 11, RefillInterval: 2 * time.Second, Burst: 31,
		},
		QueryActor: config.RepositorySearchRateBucketConfig{
			RefillTokens: 7, RefillInterval: 3 * time.Second, Burst: 17,
		},
		BuildProject: config.RepositorySearchRateBucketConfig{
			RefillTokens: 5, RefillInterval: 13 * time.Second, Burst: 19,
		},
		BuildActor: config.RepositorySearchRateBucketConfig{
			RefillTokens: 3, RefillInterval: 29 * time.Second, Burst: 23,
		},
	}
	wantBucket := func(value config.RepositorySearchRateBucketConfig) repository.ExactTreeSearchAdmissionBucketLimits {
		return repository.ExactTreeSearchAdmissionBucketLimits{
			RefillTokens: value.RefillTokens, RefillInterval: value.RefillInterval, Burst: value.Burst,
		}
	}
	want := repository.RedisExactTreeSearchAdmissionOptions{
		Prefix: configured.RedisPrefix, Timeout: configured.Timeout,
		Query: repository.ExactTreeSearchAdmissionOperationLimits{
			Project: wantBucket(configured.QueryProject), Actor: wantBucket(configured.QueryActor),
		},
		FirstBuilder: repository.ExactTreeSearchAdmissionOperationLimits{
			Project: wantBucket(configured.BuildProject), Actor: wantBucket(configured.BuildActor),
		},
	}
	if got := repositorySearchAdmissionOptions(configured); got != want {
		t.Fatalf("repository search admission options = %#v, want %#v", got, want)
	}
}
