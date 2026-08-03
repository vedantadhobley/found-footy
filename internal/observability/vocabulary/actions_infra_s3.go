// S3-adapter Action enum values + init-time registration.
package vocabulary

// S3-adapter actions. Cover the client lifecycle + per-operation
// outcomes (upload/download/head/delete/presign). Operation-level
// actions get emitted at DEBUG on success and WARN on failure.
const (
	ActionS3Connected      Action = "s3_connected"       // initial bucket-head probe succeeded
	ActionS3ConnectFailed  Action = "s3_connect_failed"  // bucket-head or client build failed
	ActionS3Upload         Action = "s3_upload"          // PutObject succeeded
	ActionS3UploadFailed   Action = "s3_upload_failed"   // PutObject returned an error
	ActionS3Download       Action = "s3_download"        // GetObject succeeded
	ActionS3DownloadFailed Action = "s3_download_failed" // GetObject returned an error
	ActionS3Head           Action = "s3_head"            // HeadObject succeeded
	ActionS3HeadMissing    Action = "s3_head_missing"    // HeadObject 404 (expected case; distinct from HeadFailed)
	ActionS3HeadFailed     Action = "s3_head_failed"     // HeadObject non-404 error
	ActionS3Delete         Action = "s3_delete"          // DeleteObject succeeded
	ActionS3DeleteFailed   Action = "s3_delete_failed"   // DeleteObject returned an error
	ActionS3Copy           Action = "s3_copy"            // CopyObject succeeded (staging→assets promote)
	ActionS3CopyFailed     Action = "s3_copy_failed"     // CopyObject returned an error
	ActionS3Presign        Action = "s3_presign"         // PresignGetObject succeeded
	ActionS3PresignFailed  Action = "s3_presign_failed"  // PresignGetObject returned an error
)

func init() {
	registerActions(
		ActionS3Connected,
		ActionS3ConnectFailed,
		ActionS3Upload,
		ActionS3UploadFailed,
		ActionS3Download,
		ActionS3DownloadFailed,
		ActionS3Head,
		ActionS3HeadMissing,
		ActionS3HeadFailed,
		ActionS3Delete,
		ActionS3DeleteFailed,
		ActionS3Copy,
		ActionS3CopyFailed,
		ActionS3Presign,
		ActionS3PresignFailed,
	)
}
