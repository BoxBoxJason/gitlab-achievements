package gitlabclient

import (
	"fmt"
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
)

// GetPipeline retrieves a single pipeline by ID.
func (c *ReadClient) GetPipeline(pid any, pipeline int64, options ...gitlab.RequestOptionFunc) (*gitlab.Pipeline, error) {
	p, _, err := c.raw.Pipelines.GetPipeline(pid, pipeline, options...)
	if err != nil {
		return nil, fmt.Errorf("failed to get pipeline: %w", err)
	}

	return p, nil
}

// ListProjectPipelines iterates every pipeline in a project matching opt.
// Set opt.Pagination = "keyset" to use keyset pagination for large
// projects; the iterator follows whichever pagination style the response
// returns.
func (c *ReadClient) ListProjectPipelines(pid any, opt *gitlab.ListProjectPipelinesOptions, options ...gitlab.RequestOptionFunc) iter.Seq2[*gitlab.PipelineInfo, error] {
	return iteratePages(func(reqOpts ...gitlab.RequestOptionFunc) ([]*gitlab.PipelineInfo, *gitlab.Response, error) {
		return c.raw.Pipelines.ListProjectPipelines(pid, opt, withExtra(options, reqOpts...)...)
	})
}

// previousPipelineScan bounds how many older pipelines on a ref
// LastPipelineOutcome looks through for one that finished. Canceled,
// skipped and manual pipelines in between are passed over, but a ref with
// a long run of them is not worth paging through history for.
const previousPipelineScan = 50

// previousPipelinePage is the page size LastPipelineOutcome asks for. The
// answer is nearly always the pipeline right before, so a small page is
// one cheap request rather than a hundred records nobody reads.
const previousPipelinePage = 20

// LastPipelineOutcome reports how the most recent pipeline on ref before
// beforeID ended: gitlab.Success or gitlab.Failed, whichever it finished
// with. Pipelines that ended neither way (canceled, skipped, still running)
// are passed over. It reports an empty status when no earlier pipeline on
// the ref finished, within previousPipelineScan of it.
func (c *ReadClient) LastPipelineOutcome(pid any, ref string, beforeID int64, options ...gitlab.RequestOptionFunc) (gitlab.BuildStateValue, error) {
	opt := &gitlab.ListProjectPipelinesOptions{
		ListOptions: gitlab.ListOptions{PerPage: previousPipelinePage},
		Ref:         &ref,
		OrderBy:     new("id"),
		Sort:        new("desc"),
	}

	scanned := 0

	for info, err := range c.ListProjectPipelines(pid, opt, options...) {
		if err != nil {
			return "", err
		}

		if info.ID >= beforeID {
			continue
		}

		switch status := gitlab.BuildStateValue(info.Status); status {
		case gitlab.Success, gitlab.Failed:
			return status, nil
		case gitlab.Created, gitlab.WaitingForResource, gitlab.Preparing, gitlab.Pending,
			gitlab.Running, gitlab.Canceled, gitlab.Skipped, gitlab.Manual, gitlab.Scheduled:
		}

		scanned++
		if scanned >= previousPipelineScan {
			break
		}
	}

	return "", nil
}
