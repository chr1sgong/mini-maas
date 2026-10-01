package ratelimit

import "github.com/mini-maas/gateway/internal/auth"

type Registry struct {
	byKey map[string]*bucketPair
}

type bucketPair struct {
	qps *Bucket
	tpm *Bucket // Define the fields for the bucket pair here
}

func NewRegistry(identities map[string]*auth.Identity) *Registry {
	bykey := make(map[string]*bucketPair, len(identities))
	for key, id := range identities {
		bykey[key] = &bucketPair{
			// QPS 限流桶, 容量=突发配额,补充速率=每秒配额
			qps: NewBucket(id.Quota.QPSBurst, id.Quota.QPS),
			// TPM 限流桶, 容量=突发配额,补充速率=每秒配额
			tpm: NewBucket(id.Quota.TPM, id.Quota.TPM/60),
		}
	}

	return &Registry{
		byKey: bykey,
	}
}

func (r *Registry) pair(key string) *bucketPair {
	return r.byKey[key]
}
