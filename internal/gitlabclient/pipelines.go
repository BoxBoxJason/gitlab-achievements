package gitlabclient

import (
	"iter"

	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
)

// GetPipeline retrieves a single pipeline by ID.
func (c *ReadClient) GetPipeline(pid any, pipeline int64, options ...gitlab.RequestOptionFunc) (*gitlab.Pipeline, error) {
	p, _, err := c.raw.Pipelines.GetPipeline(pid, pipeline, options...)

	return p, err
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
