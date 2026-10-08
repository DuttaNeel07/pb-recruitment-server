package services

import (
	"app/internal/judge0"
	"app/internal/models"
	"app/internal/models/dto"
	"app/internal/s3"
	"app/internal/stores"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type submissionTestStore struct{ *stores.SubmissionStore }

func (submissionTestStore) CreateSubmission(context.Context, *models.Submission) (string, error) {
	return "submission", nil
}

type problemTestStore struct{ *stores.ProblemStore }

func (problemTestStore) GetProblem(context.Context, string, string) (*dto.GetProblemStatementResponse, error) {
	return &dto.GetProblemStatementResponse{TimeLimit: 1000, MemoryLimit: 256}, nil
}

type executionTestStore struct {
	*stores.ExecutionStore
	saveTokens func(context.Context, map[string]string) error
	markFailed func(context.Context, []string) error
}

func (executionTestStore) InsertBatch(context.Context, string, []int) ([]models.Execution, error) {
	return []models.Execution{{ID: "execution"}}, nil
}

func (s executionTestStore) SaveTokens(ctx context.Context, tokens map[string]string) error {
	return s.saveTokens(ctx, tokens)
}

func (s executionTestStore) MarkFailed(ctx context.Context, ids []string) error {
	return s.markFailed(ctx, ids)
}

func TestCreateSubmissionFailureBookkeeping(t *testing.T) {
	writeErr := errors.New("failure status write failed")
	for _, tc := range []struct {
		name        string
		timeoutSave bool
		markErr     error
	}{
		{name: "token save timeout", timeoutSave: true},
		{name: "failure status write error", markErr: writeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					w.WriteHeader(http.StatusOK)
				} else if strings.HasSuffix(r.URL.Path, "testcases.json") {
					w.Write([]byte(`[{"index":0,"input":"1"}]`))
				} else {
					w.Write([]byte(`["1"]`))
				}
			}))
			defer objects.Close()
			for key, value := range map[string]string{
				"AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test", "AWS_SESSION_TOKEN": "",
				"AWS_REGION": "us-east-1", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
				"AWS_CONFIG_FILE": "/dev/null", "AWS_EC2_METADATA_DISABLED": "true",
				"S3_SUBMISSIONS_BUCKET": "test", "AWS_ENDPOINT_URL_S3": objects.URL,
			} {
				t.Setenv(key, value)
			}
			judge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.timeoutSave {
					w.WriteHeader(http.StatusCreated)
					w.Write([]byte(`[{"token":"token"}]`))
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer judge.Close()
			t.Setenv("JUDGE0_URL", judge.URL)
			t.Setenv("JUDGE0_AUTH_TOKEN", "")
			t.Setenv("JUDGE0_CALLBACK_BASE_URL", "")
			t.Setenv("JUDGE0_TIMEOUT_MS", "1000")
			t.Setenv("JUDGE0_BATCH_SIZE", "20")

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			marked := false
			store := &stores.Storage{
				Submissions: submissionTestStore{}, Problems: problemTestStore{},
				Executions: executionTestStore{
					saveTokens: func(ctx context.Context, tokens map[string]string) error {
						if !tc.timeoutSave || tokens["execution"] != "token" {
							t.Fatal("unexpected token save")
						}
						cancel()
						<-ctx.Done()
						return ctx.Err()
					},
					markFailed: func(ctx context.Context, ids []string) error {
						marked = true
						if err := ctx.Err(); err != nil {
							t.Errorf("failure bookkeeping received expired context: %v", err)
							return err
						}
						if _, bounded := ctx.Deadline(); !bounded {
							t.Error("failure bookkeeping needs a bounded context")
						}
						if len(ids) != 1 || ids[0] != "execution" {
							t.Errorf("failed execution IDs = %v", ids)
						}
						return tc.markErr
					},
				},
			}
			service := NewSubmissionService(store, s3.NewS3Client(), judge0.NewClient())
			id, err := service.CreateSubmission(ctx, "user", models.Code, &dto.SubmitSubmissionRequest{
				ContestID: "contest", ProblemID: "problem", Language: "cpp",
				Code: base64.StdEncoding.EncodeToString([]byte("int main(){}")),
			})
			if !marked {
				t.Error("failure bookkeeping was not called")
			}
			if !errors.Is(err, tc.markErr) {
				t.Errorf("CreateSubmission error = %v, want %v", err, tc.markErr)
			}
			if tc.markErr == nil && id != "submission" {
				t.Errorf("submission ID = %q", id)
			}
		})
	}
}
