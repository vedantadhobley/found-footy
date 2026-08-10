// Read-API (Chi handlers) Action enum values + init-time registration.
package vocabulary

const (
	ActionAPIRequestFailed      Action = "api_request_failed"       // a handler hit an internal error → 500
	ActionAPIShareForeignBucket Action = "api_share_foreign_bucket" // a resolved share's asset bucket != the API's configured bucket (presign would sign the wrong bucket)
)

func init() {
	registerActions(
		ActionAPIRequestFailed,
		ActionAPIShareForeignBucket,
	)
}
