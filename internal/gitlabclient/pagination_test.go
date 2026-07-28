package gitlabclient

import (
	"errors"
	"reflect"
	"testing"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

func TestIteratePages_MultiPage(t *testing.T) {
	calls := 0
	call := func(options ...gitlab.RequestOptionFunc) ([]string, *gitlab.Response, error) {
		calls++

		switch calls {
		case 1:
			return []string{"a", "b"}, &gitlab.Response{NextPage: 2}, nil
		case 2:
			return []string{"c"}, &gitlab.Response{}, nil
		default:
			t.Fatalf("unexpected call %d", calls)

			return nil, nil, nil
		}
	}

	var got []string

	for item, err := range iteratePages(call) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got = append(got, item)
	}

	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
}

func TestIteratePages_StopsEarly(t *testing.T) {
	calls := 0
	call := func(options ...gitlab.RequestOptionFunc) ([]string, *gitlab.Response, error) {
		calls++

		return []string{"a", "b", "c"}, &gitlab.Response{NextPage: 2}, nil
	}

	var got []string

	for item, err := range iteratePages(call) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got = append(got, item)
		if len(got) == 2 {
			break
		}
	}

	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if calls != 1 {
		t.Errorf("expected iteration to stop before requesting a second page, got %d calls", calls)
	}
}

func TestIteratePages_YieldsTerminalError(t *testing.T) {
	wantErr := errors.New("boom")
	call := func(options ...gitlab.RequestOptionFunc) ([]string, *gitlab.Response, error) {
		return nil, nil, wantErr
	}

	var (
		yields  int
		gotErr  error
		gotItem string
	)

	for item, err := range iteratePages(call) {
		yields++
		gotItem = item
		gotErr = err
	}

	if yields != 1 {
		t.Fatalf("expected exactly one yielded value on error, got %d", yields)
	}

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("got err %v, want %v", gotErr, wantErr)
	}

	if gotItem != "" {
		t.Errorf("expected zero value item alongside terminal error, got %q", gotItem)
	}
}

func TestIteratePages_ErrorOnLaterPage(t *testing.T) {
	wantErr := errors.New("boom")
	calls := 0
	call := func(options ...gitlab.RequestOptionFunc) ([]string, *gitlab.Response, error) {
		calls++

		if calls == 1 {
			return []string{"a"}, &gitlab.Response{NextPage: 2}, nil
		}

		return nil, nil, wantErr
	}

	var (
		got    []string
		gotErr error
	)

	for item, err := range iteratePages(call) {
		if err != nil {
			gotErr = err

			continue
		}

		got = append(got, item)
	}

	if want := []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("got err %v, want %v", gotErr, wantErr)
	}
}
