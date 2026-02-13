// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"testing"

	issues_model "code.gitea.io/gitea/models/issues"
	user_model "code.gitea.io/gitea/models/user"

	"github.com/stretchr/testify/assert"
)

func makeIssue(id int64, closed bool) *issues_model.Issue {
	return &issues_model.Issue{
		ID:       id,
		IsClosed: closed,
		Poster:   &user_model.User{ID: 1},
	}
}

func TestComputeDescendantCounts(t *testing.T) {
	// Tree: root (open) -> child1 (open) -> grandchild (closed)
	//                    -> child2 (closed)
	grandchild := &IssueNode{Issue: makeIssue(4, true), Level: 2}
	child1 := &IssueNode{Issue: makeIssue(2, false), Children: []*IssueNode{grandchild}, Level: 1}
	child2 := &IssueNode{Issue: makeIssue(3, true), Level: 1}
	root := &IssueNode{Issue: makeIssue(1, false), Children: []*IssueNode{child1, child2}, Level: 0}

	root.ComputeDescendantCounts()

	// Only child1 is an open descendant (grandchild is closed, child2 is closed)
	assert.Equal(t, 1, root.OpenDescendants)
	// child2 and grandchild are closed descendants
	assert.Equal(t, 2, root.ClosedDescendants)
	// Check child1 descendant counts
	assert.Equal(t, 0, child1.OpenDescendants)
	assert.Equal(t, 1, child1.ClosedDescendants)
}

func TestMatchesFilter(t *testing.T) {
	t.Run("NoFilters", func(t *testing.T) {
		node := &IssueNode{Issue: makeIssue(1, false)}
		assert.True(t, node.MatchesFilter("all", nil, 0, 0))
	})

	t.Run("StateFilterOpen", func(t *testing.T) {
		node := &IssueNode{Issue: makeIssue(1, false)}
		assert.True(t, node.MatchesFilter("open", nil, 0, 0))
		assert.False(t, node.MatchesFilter("closed", nil, 0, 0))
	})

	t.Run("StateFilterClosed", func(t *testing.T) {
		node := &IssueNode{Issue: makeIssue(1, true)}
		assert.True(t, node.MatchesFilter("closed", nil, 0, 0))
		assert.False(t, node.MatchesFilter("open", nil, 0, 0))
	})

	t.Run("StateFilterDescendant", func(t *testing.T) {
		// Parent is open, child is closed -> parent shown when filtering closed
		child := &IssueNode{Issue: makeIssue(2, true), Level: 1}
		parent := &IssueNode{Issue: makeIssue(1, false), Children: []*IssueNode{child}, Level: 0}
		assert.True(t, parent.MatchesFilter("closed", nil, 0, 0))
	})

	t.Run("LabelFilterMatch", func(t *testing.T) {
		issue := makeIssue(1, false)
		issue.Labels = []*issues_model.Label{{ID: 1}}
		node := &IssueNode{Issue: issue}
		assert.True(t, node.MatchesFilter("all", []int64{1}, 0, 0))
	})

	t.Run("LabelFilterNoMatch", func(t *testing.T) {
		issue := makeIssue(1, false)
		issue.Labels = []*issues_model.Label{{ID: 2}}
		node := &IssueNode{Issue: issue}
		assert.False(t, node.MatchesFilter("all", []int64{1}, 0, 0))
	})

	t.Run("AssigneeFilterMatch", func(t *testing.T) {
		issue := makeIssue(1, false)
		issue.Assignees = []*user_model.User{{ID: 5}}
		node := &IssueNode{Issue: issue}
		assert.True(t, node.MatchesFilter("all", nil, 5, 0))
	})

	t.Run("AssigneeFilterNoMatch", func(t *testing.T) {
		issue := makeIssue(1, false)
		// No assignees
		node := &IssueNode{Issue: issue}
		assert.False(t, node.MatchesFilter("all", nil, 5, 0))
	})

	t.Run("MilestoneFilterMatch", func(t *testing.T) {
		issue := makeIssue(1, false)
		issue.MilestoneID = 3
		node := &IssueNode{Issue: issue}
		assert.True(t, node.MatchesFilter("all", nil, 0, 3))
	})

	t.Run("MilestoneFilterNoMilestone", func(t *testing.T) {
		issue := makeIssue(1, false)
		issue.MilestoneID = 0
		node := &IssueNode{Issue: issue}
		// milestoneID == -1 means "filter for issues with no milestone"
		assert.True(t, node.MatchesFilter("all", nil, 0, -1))
	})

	t.Run("DescendantMatches", func(t *testing.T) {
		parentIssue := makeIssue(1, false)
		// Parent has no labels

		childIssue := makeIssue(2, false)
		childIssue.Labels = []*issues_model.Label{{ID: 1}}

		child := &IssueNode{Issue: childIssue, Level: 1}
		parent := &IssueNode{Issue: parentIssue, Children: []*IssueNode{child}, Level: 0}

		// Parent doesn't match label filter, but child does -> true
		assert.True(t, parent.MatchesFilter("all", []int64{1}, 0, 0))
	})
}

func TestFilterTree(t *testing.T) {
	t.Run("FilterByLabel", func(t *testing.T) {
		rootIssue := makeIssue(1, false)
		rootIssue.Labels = []*issues_model.Label{{ID: 1}}

		childIssue := makeIssue(2, false)
		childIssue.Labels = []*issues_model.Label{{ID: 2}}

		grandchildIssue := makeIssue(3, false)
		grandchildIssue.Labels = []*issues_model.Label{{ID: 1}}

		grandchild := &IssueNode{Issue: grandchildIssue, Level: 2}
		child := &IssueNode{Issue: childIssue, Children: []*IssueNode{grandchild}, Level: 1}
		root := &IssueNode{Issue: rootIssue, Children: []*IssueNode{child}, Level: 0}

		// Filter for label 1: root matches, child kept because grandchild matches
		filtered := root.FilterTree("all", []int64{1}, 0, 0)
		assert.NotNil(t, filtered)
		assert.Equal(t, int64(1), filtered.Issue.ID)
		assert.Len(t, filtered.Children, 1)
		assert.Equal(t, int64(2), filtered.Children[0].Issue.ID)
		assert.Len(t, filtered.Children[0].Children, 1)
		assert.Equal(t, int64(3), filtered.Children[0].Children[0].Issue.ID)
	})

	t.Run("FilterNoMatch", func(t *testing.T) {
		rootIssue := makeIssue(1, false)
		rootIssue.Labels = []*issues_model.Label{{ID: 1}}

		childIssue := makeIssue(2, false)
		childIssue.Labels = []*issues_model.Label{{ID: 2}}

		child := &IssueNode{Issue: childIssue, Level: 1}
		root := &IssueNode{Issue: rootIssue, Children: []*IssueNode{child}, Level: 0}

		// Filter for label 3: nothing matches
		filtered := root.FilterTree("all", []int64{3}, 0, 0)
		assert.Nil(t, filtered)
	})

	t.Run("FilterByStateClosed", func(t *testing.T) {
		// root (open) -> child1 (closed) -> grandchild (closed)
		//             -> child2 (open)
		grandchild := &IssueNode{Issue: makeIssue(4, true), Level: 2}
		child1 := &IssueNode{Issue: makeIssue(2, true), Children: []*IssueNode{grandchild}, Level: 1}
		child2 := &IssueNode{Issue: makeIssue(3, false), Level: 1}
		root := &IssueNode{Issue: makeIssue(1, false), Children: []*IssueNode{child1, child2}, Level: 0}

		root.ComputeDescendantCounts()

		// Filter by closed: root should be shown (has closed descendants)
		// child1 matches, child2 does not
		filtered := root.FilterTree("closed", nil, 0, 0)
		assert.NotNil(t, filtered)
		assert.Len(t, filtered.Children, 1)
		assert.Equal(t, int64(2), filtered.Children[0].Issue.ID)
		// Precomputed counts preserved from original tree
		assert.Equal(t, 1, filtered.OpenDescendants)
		assert.Equal(t, 2, filtered.ClosedDescendants)
	})
}

func TestBuildIssueTree(t *testing.T) {
	issue1 := makeIssue(1, false)
	issue2 := makeIssue(2, false)
	issue3 := makeIssue(3, true)

	issueMap := map[int64]*issues_model.Issue{
		1: issue1,
		2: issue2,
		3: issue3,
	}

	// Issue 1 depends on issues 2 and 3
	depsByIssueID := map[int64][]int64{
		1: {2, 3},
	}

	node := buildIssueTree(issue1, issueMap, depsByIssueID, make(map[int64]bool), 0)

	assert.NotNil(t, node)
	assert.Equal(t, int64(1), node.Issue.ID)
	assert.Equal(t, 0, node.Level)
	assert.Len(t, node.Children, 2)

	// Children should be issue2 and issue3 at level 1
	childIDs := map[int64]bool{}
	for _, child := range node.Children {
		childIDs[child.Issue.ID] = true
		assert.Equal(t, 1, child.Level)
	}
	assert.True(t, childIDs[2])
	assert.True(t, childIDs[3])
}

func TestBuildIssueTreeCyclicDeps(t *testing.T) {
	issue1 := makeIssue(1, false)
	issue2 := makeIssue(2, false)

	issueMap := map[int64]*issues_model.Issue{
		1: issue1,
		2: issue2,
	}

	// Circular dependency: 1 -> 2 -> 1
	depsByIssueID := map[int64][]int64{
		1: {2},
		2: {1},
	}

	node := buildIssueTree(issue1, issueMap, depsByIssueID, make(map[int64]bool), 0)

	// Should not infinite-recurse; issue1 is root with issue2 as child
	assert.NotNil(t, node)
	assert.Equal(t, int64(1), node.Issue.ID)
	assert.Len(t, node.Children, 1)
	assert.Equal(t, int64(2), node.Children[0].Issue.ID)

	// issue2's child list should NOT contain issue1 again (visited guard)
	assert.Empty(t, node.Children[0].Children)
}
