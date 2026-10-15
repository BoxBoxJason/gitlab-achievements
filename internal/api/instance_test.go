package api

import (
	"net/http"
	"testing"

	"github.com/boxboxjason/gitlab-achievements/internal/gitlabclient"
)

func TestInstance_DescribesTheDeployment(t *testing.T) {
	want := Instance{GitLabURL: "https://gitlab.example.com", Version: "v1.2.3"}

	rec := get(t, mustNew(testConn(t), Options{Instance: want}), InstancePath)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if got := decode[Instance](t, rec); got != want {
		t.Errorf("expected %+v, got %+v", want, got)
	}
}

func TestInstance_SitsBehindAuthentication(t *testing.T) {
	api := mustNew(testConn(t), Options{
		Verifier: &stubVerifier{identity: &gitlabclient.Identity{ID: 1}},
		Instance: Instance{GitLabURL: "https://gitlab.example.com"},
	})

	if rec := get(t, api, InstancePath); rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without a credential, got %d", rec.Code)
	}

	if rec := get(t, api, InstancePath, bearer); rec.Code != http.StatusOK {
		t.Errorf("expected 200 with one, got %d", rec.Code)
	}
}
