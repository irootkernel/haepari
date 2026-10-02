package controller

// Nil in production. Native child-process tests stop at durable/effect seams.
var retentionBoundary func(string)

func observeRetentionBoundary(stage string) {
	if retentionBoundary != nil {
		retentionBoundary(stage)
	}
}
