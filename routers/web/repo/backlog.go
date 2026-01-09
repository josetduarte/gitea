// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"
	"strconv"

	"code.gitea.io/gitea/models/db"
	issues_model "code.gitea.io/gitea/models/issues"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/modules/optional"
	"code.gitea.io/gitea/modules/templates"
	"code.gitea.io/gitea/routers/web/shared/issue"
	shared_user "code.gitea.io/gitea/routers/web/shared/user"
	"code.gitea.io/gitea/services/context"
)

const (
	tplBacklog templates.TplName = "repo/backlog/view"
)

// IssueNode represents an issue in a tree hierarchy
type IssueNode struct {
	Issue    *issues_model.Issue
	Children []*IssueNode
	Level    int
}

// CountOpenDescendants recursively counts all open descendant issues
func (n *IssueNode) CountOpenDescendants() int {
	count := 0
	for _, child := range n.Children {
		if !child.Issue.IsClosed {
			count++
		}
		count += child.CountOpenDescendants()
	}
	return count
}

// CountClosedDescendants recursively counts all closed descendant issues
func (n *IssueNode) CountClosedDescendants() int {
	count := 0
	for _, child := range n.Children {
		if child.Issue.IsClosed {
			count++
		}
		count += child.CountClosedDescendants()
	}
	return count
}

// MatchesFilter checks if this node or any of its descendants match the filter criteria
func (n *IssueNode) MatchesFilter(labelIDs []int64, assigneeID int64, milestoneID int64) bool {
	// Check if this node matches
	if nodeMatchesFilter(n.Issue, labelIDs, assigneeID, milestoneID) {
		return true
	}
	// Check if any descendant matches
	for _, child := range n.Children {
		if child.MatchesFilter(labelIDs, assigneeID, milestoneID) {
			return true
		}
	}
	return false
}

// nodeMatchesFilter checks if an issue matches the filter criteria
func nodeMatchesFilter(issue *issues_model.Issue, labelIDs []int64, assigneeID int64, milestoneID int64) bool {
	// If no filters, everything matches
	if len(labelIDs) == 0 && assigneeID == 0 && milestoneID == 0 {
		return true
	}

	// Check milestone filter
	if milestoneID != 0 {
		if milestoneID == -1 {
			// Filter for issues with no milestone
			if issue.MilestoneID != 0 {
				return false
			}
		} else {
			// Filter for specific milestone
			if issue.MilestoneID != milestoneID {
				return false
			}
		}
	}

	// Check label filter
	if len(labelIDs) > 0 {
		issueLabelMap := make(map[int64]bool)
		for _, label := range issue.Labels {
			issueLabelMap[label.ID] = true
		}
		hasMatchingLabel := false
		for _, labelID := range labelIDs {
			if issueLabelMap[labelID] {
				hasMatchingLabel = true
				break
			}
		}
		if !hasMatchingLabel {
			return false
		}
	}

	// Check assignee filter
	if assigneeID != 0 {
		hasMatchingAssignee := false
		for _, assignee := range issue.Assignees {
			if assignee.ID == assigneeID {
				hasMatchingAssignee = true
				break
			}
		}
		if !hasMatchingAssignee {
			return false
		}
	}

	return true
}

// FilterTree filters the tree to only include nodes that match the filter or have matching descendants
func (n *IssueNode) FilterTree(labelIDs []int64, assigneeID int64, milestoneID int64) *IssueNode {
	if !n.MatchesFilter(labelIDs, assigneeID, milestoneID) {
		return nil
	}

	// Create new node with filtered children
	filteredNode := &IssueNode{
		Issue: n.Issue,
		Level: n.Level,
	}

	for _, child := range n.Children {
		if filteredChild := child.FilterTree(labelIDs, assigneeID, milestoneID); filteredChild != nil {
			filteredNode.Children = append(filteredNode.Children, filteredChild)
		}
	}

	return filteredNode
}

// Backlog shows the backlog view with tree hierarchy by dependencies
func Backlog(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.issues.backlog")
	ctx.Data["PageIsBacklog"] = true

	// Get filter parameters
	milestoneID := ctx.FormInt64("milestone")
	assigneeID := ctx.FormString("assignee")

	// Get label filter
	labelFilter := issue.PrepareFilterIssueLabels(ctx, ctx.Repo.Repository.ID, ctx.Repo.Owner)
	if ctx.Written() {
		return
	}
	ctx.Data["SelLabelIDs"] = labelFilter.SelectedLabelIDs

	// Get assignees for the filter dropdown
	assigneeUsers, err := repo_model.GetRepoAssignees(ctx, ctx.Repo.Repository)
	if err != nil {
		ctx.ServerError("GetRepoAssignees", err)
		return
	}
	ctx.Data["Assignees"] = shared_user.MakeSelfOnTop(ctx.Doer, assigneeUsers)
	ctx.Data["AssigneeID"] = assigneeID

	// Parse assigneeID as int64 for filtering
	assigneeIDInt, _ := strconv.ParseInt(assigneeID, 10, 64)

	// Get all milestones for the filter dropdown
	milestones, err := db.Find[issues_model.Milestone](ctx, issues_model.FindMilestoneOptions{
		RepoID: ctx.Repo.Repository.ID,
	})
	if err != nil {
		ctx.ServerError("GetMilestones", err)
		return
	}

	openMilestones, closedMilestones := issues_model.MilestoneList{}, issues_model.MilestoneList{}
	for _, milestone := range milestones {
		if milestone.IsClosed {
			closedMilestones = append(closedMilestones, milestone)
		} else {
			openMilestones = append(openMilestones, milestone)
		}
	}
	ctx.Data["OpenMilestones"] = openMilestones
	ctx.Data["ClosedMilestones"] = closedMilestones
	ctx.Data["MilestoneID"] = milestoneID

	// Build issue options - get ALL open issues (milestone filtering done via tree pruning)
	issueOpts := &issues_model.IssuesOptions{
		RepoIDs:  []int64{ctx.Repo.Repository.ID},
		IsPull:   optional.Some(false),
		IsClosed: optional.Some(false),
		SortType: "priority",
	}

	// Get all open issues
	issues, err := issues_model.Issues(ctx, issueOpts)
	if err != nil {
		ctx.ServerError("Issues", err)
		return
	}

	// Load attributes for all issues
	for _, issue := range issues {
		if err := issue.LoadAttributes(ctx); err != nil {
			ctx.ServerError("LoadAttributes", err)
			return
		}
	}

	// Build issue tree based on dependencies
	// Create issue map for quick lookup
	issueMap := make(map[int64]*issues_model.Issue)
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// Find root issues (parent issues that depend on others)
	// Root = issues that are NOT dependencies of other issues
	// If issue A depends on issue B, then A is parent (root) and B is child (dependency)
	isChild := make(map[int64]bool)
	for _, issue := range issues {
		// Get issues this issue depends on (blocked by)
		blockedBy, err := issue.BlockedByDependencies(ctx, db.ListOptions{})
		if err == nil {
			for _, dep := range blockedBy {
				// Mark the dependencies as children
				if _, exists := issueMap[dep.Issue.ID]; exists {
					isChild[dep.Issue.ID] = true
				}
			}
		}
	}

	// Root issues are those not marked as children (issues that depend on things, or standalone)
	rootIssues := make([]*issues_model.Issue, 0)
	for _, issue := range issues {
		if !isChild[issue.ID] {
			rootIssues = append(rootIssues, issue)
		}
	}

	// Build tree recursively (allow issues to appear under multiple parents)
	tree := make([]*IssueNode, 0)
	for _, issue := range rootIssues {
		node := buildIssueTree(ctx, issue, issueMap, make(map[int64]bool), 0)
		if node != nil {
			tree = append(tree, node)
		}
	}

	// Filter tree based on milestone, label and assignee filters
	if milestoneID != 0 || len(labelFilter.SelectedLabelIDs) > 0 || assigneeIDInt != 0 {
		filteredTree := make([]*IssueNode, 0)
		for _, node := range tree {
			if filteredNode := node.FilterTree(labelFilter.SelectedLabelIDs, assigneeIDInt, milestoneID); filteredNode != nil {
				filteredTree = append(filteredTree, filteredNode)
			}
		}
		tree = filteredTree
	}

	ctx.Data["IssueTree"] = tree

	// Statistics
	ctx.Data["IssueStats"] = getBacklogStats(ctx, ctx.Repo.Repository.ID)

	ctx.HTML(http.StatusOK, tplBacklog)
}

// buildIssueTree recursively builds a tree where parents depend on children
// An issue that depends on others is the parent, the dependencies are children
func buildIssueTree(ctx *context.Context, issue *issues_model.Issue, issueMap map[int64]*issues_model.Issue, visited map[int64]bool, level int) *IssueNode {
	// Prevent infinite recursion
	if visited[issue.ID] {
		return nil
	}
	visited[issue.ID] = true

	node := &IssueNode{
		Issue:    issue,
		Children: make([]*IssueNode, 0),
		Level:    level,
	}

	// Get issues that this issue depends on (blocked by)
	// This issue is the parent, the issues it depends on are shown as children
	blockedBy, err := issue.BlockedByDependencies(ctx, db.ListOptions{})
	if err == nil {
		for _, dep := range blockedBy {
			if childIssue, exists := issueMap[dep.Issue.ID]; exists {
				// Create a new visited map for each child to allow multiple parent paths
				childVisited := make(map[int64]bool)
				for k, v := range visited {
					childVisited[k] = v
				}
				childNode := buildIssueTree(ctx, childIssue, issueMap, childVisited, level+1)
				if childNode != nil {
					node.Children = append(node.Children, childNode)
				}
			}
		}
	}

	return node
}

// getBacklogStats returns statistics for the backlog
func getBacklogStats(ctx *context.Context, repoID int64) map[string]int64 {
	stats := make(map[string]int64)

	// Count total open issues
	total, err := issues_model.CountIssues(ctx, &issues_model.IssuesOptions{
		RepoIDs:  []int64{repoID},
		IsPull:   optional.Some(false),
		IsClosed: optional.Some(false),
	}, nil)
	if err == nil {
		stats["Total"] = total
	}

	// Count issues without milestone
	noMilestone, err := issues_model.CountIssues(ctx, &issues_model.IssuesOptions{
		RepoIDs:      []int64{repoID},
		IsPull:       optional.Some(false),
		IsClosed:     optional.Some(false),
		MilestoneIDs: []int64{db.NoConditionID},
	}, nil)
	if err == nil {
		stats["NoMilestone"] = noMilestone
	}

	// Count issues with dependencies
	var withDeps int64
	_, err = db.GetEngine(ctx).
		Table("issue").
		Join("INNER", "issue_dependency", "issue_dependency.issue_id = issue.id OR issue_dependency.dependency_id = issue.id").
		Where("issue.repo_id = ? AND issue.is_closed = ? AND issue.is_pull = ?", repoID, false, false).
		Distinct("issue.id").
		Count(&withDeps)
	if err == nil {
		stats["WithDependencies"] = withDeps
	}

	return stats
}
