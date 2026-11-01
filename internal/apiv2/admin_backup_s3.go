package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/backup"
)

// AdminBackupS3TestBody is the bucket to test, as the backup.s3.* settings
// would hold it.
type AdminBackupS3TestBody struct {
	Endpoint  string `json:"endpoint" maxLength:"2048" doc:"The S3 API endpoint, such as https://s3.us-east-1.amazonaws.com. It is not validated as a URI here: an endpoint the test cannot reach fails with step endpoint"`
	Region    string `json:"region,omitempty" maxLength:"128"`
	Bucket    string `json:"bucket" maxLength:"255"`
	KeyPrefix string `json:"key_prefix,omitempty" maxLength:"1024" doc:"The folder in the bucket; backups go under <key_prefix>/backups/"`
	AccessKey string `json:"access_key,omitempty" maxLength:"1024"`
	SecretKey string `json:"secret_key,omitempty" maxLength:"4096" writeOnly:"true" doc:"Omitted uses the stored backup.s3.secret_key, but only when endpoint and access_key are the stored ones"`
	PathStyle bool   `json:"path_style,omitempty" doc:"Path-style addressing (endpoint/bucket/key) instead of virtual-hosted (bucket.endpoint/key)"`
}

type AdminBackupS3TestInput struct {
	Body AdminBackupS3TestBody
}

// AdminBackupS3TestResult is a connection test's outcome. A failed test is a
// 200 with ok false: the test ran, and the bucket answered (or did not).
type AdminBackupS3TestResult struct {
	OK      bool   `json:"ok"`
	Step    string `json:"step,omitempty" enum:"endpoint,credentials,bucket,permission" doc:"The step that failed: endpoint, the address cannot be reached; credentials, the keys were refused; bucket, no such bucket or the wrong region; permission, the keys cannot write and delete under the folder"`
	Message string `json:"message" doc:"What happened, for display: Connected. Silo wrote and deleted a test file. or what failed"`
}

type AdminBackupS3TestOutput struct{ Body AdminBackupS3TestResult }

type AdminBackupAcknowledgeKeyBody struct {
	KeyCheck string `json:"key_check" pattern:"^[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}$" doc:"The check value the admin was shown (key_check from the backup list). It must be this server's current one"`
}

type AdminBackupAcknowledgeKeyInput struct {
	Body AdminBackupAcknowledgeKeyBody
}

type AdminBackupAcknowledgeKeyOutput struct {
	Body struct {
		KeyCheck string `json:"key_check" doc:"The check value now confirmed"`
	}
}

func registerAdminBackupS3(reg *Registry, op func(method, path, id, summary string) Operation) {
	test := op(http.MethodPost, "/s3/test", "testAdminBackupS3",
		"Test a bucket for backups: reach it (HeadBucket), then write and delete a test object under the key prefix. A failed test answers ok false with the step that failed. Nothing is saved.")
	test.RetrySafety = RetrySafetyNaturalIdempotent
	Register(reg, test, func(ctx context.Context, in *AdminBackupS3TestInput) (*AdminBackupS3TestOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		b := in.Body
		result, err := svc.TestS3(ctx, backup.S3Config{
			Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Bucket, KeyPrefix: b.KeyPrefix,
			AccessKey: b.AccessKey, SecretKey: b.SecretKey, PathStyle: b.PathStyle,
		})
		if err != nil {
			return nil, backupProblem(err)
		}
		return &AdminBackupS3TestOutput{Body: AdminBackupS3TestResult{OK: result.OK, Step: result.Step, Message: result.Message}}, nil
	})

	ack := op(http.MethodPost, "/s3/acknowledge-key", "acknowledgeAdminBackupKey",
		"Confirm that a copy of this server's SECRET_KEY is kept somewhere other than this server. backup.destination can be set to s3 only after this, and S3 backups pause when SECRET_KEY changes until it is confirmed again. 409 when key_check is not this server's current check value.")
	ack.RetrySafety = RetrySafetyNaturalIdempotent
	ack.Errors = []int{http.StatusConflict}
	Register(reg, ack, func(ctx context.Context, in *AdminBackupAcknowledgeKeyInput) (*AdminBackupAcknowledgeKeyOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		if err := svc.AcknowledgeKey(ctx, in.Body.KeyCheck); err != nil {
			return nil, backupProblem(err)
		}
		out := &AdminBackupAcknowledgeKeyOutput{}
		out.Body.KeyCheck = in.Body.KeyCheck
		return out, nil
	})
}
