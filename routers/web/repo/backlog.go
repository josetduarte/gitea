// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"net/http"

	"code.gitea.io/gitea/models/db"
	issues_model "code.gitea.io/gitea/models/issues"
	"code.gitea.io/gitea/modules/optional"
	"code.gitea.io/gitea/modules/templates"
	"code.gitea.io/gitea/services/context"
)

const (
	tplBacklog templates.TplName = "repo/issue/backlog"
)

// IssueNode represents an issue in a tree hierarchy
type IssueNode struct {
	Issue    *issues_model.Issue
	Children []*IssueNode
	Level    int
}

// Backlog shows the backlog view with tree hierarchy by dependencies
func Backlog(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("repo.issues.backlog")
	ctx.Data["PageIsIssueList"] = true
	ctx.Data["PageIsBacklog"] = true

	// Get filter parameters
	milestoneID := ctx.FormInt64("milestone")

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

	// Build issue options for filtering
	issueOpts := &issues_model.IssuesOptions{
		RepoIDs:  []int64{ctx.Repo.Repository.ID},
		IsPull:   optional.Some(false),
		IsClosed: optional.Some(false),
		SortType: "priority",
	}

	if milestoneID > 0 {
		issueOpts.MilestoneIDs = []int64{milestoneID}
	} else if milestoneID == -1 {
		issueOpts.MilestoneIDs = []int64{db.NoConditionID}
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
