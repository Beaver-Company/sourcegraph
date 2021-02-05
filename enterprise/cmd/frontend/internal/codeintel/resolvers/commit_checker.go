package resolvers

import "context"

// TODO - document
type cachedCommitChecker struct {
	gitserverClient GitserverClient
	cache           map[int]map[string]bool
}

func newCachedCommitChecker(gitserverClient GitserverClient) *cachedCommitChecker {
	return &cachedCommitChecker{
		gitserverClient: gitserverClient,
		cache:           map[int]map[string]bool{},
	}
}

// TODO - document
func (c *cachedCommitChecker) Set(repositoryID int, commit string) {
	if _, ok := c.cache[repositoryID]; !ok {
		c.cache[repositoryID] = map[string]bool{}
	}

	c.cache[repositoryID][commit] = true
}

// TODO - document
func (c *cachedCommitChecker) Exists(ctx context.Context, repositoryID int, commit string) (bool, error) {
	if _, ok := c.cache[repositoryID]; !ok {
		c.cache[repositoryID] = map[string]bool{}
	}

	if exists, ok := c.cache[repositoryID][commit]; ok {
		return exists, nil
	}

	exists, err := c.gitserverClient.CommitExists(ctx, repositoryID, commit)
	if err != nil {
		return false, err
	}

	c.cache[repositoryID][commit] = exists
	return exists, nil
}
