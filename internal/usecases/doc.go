// Package usecases holds cross-domain orchestration — flows that touch
// more than one domain (event + video, fixture + event, etc.) and
// therefore don't live inside any single domain package. Example:
// VARRemoveEvent removes an event and cascades to its video_shares.
// See §4 usecases.
package usecases
