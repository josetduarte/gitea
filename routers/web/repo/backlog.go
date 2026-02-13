// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"code.gitea.io/gitea/models/db"
	issues_model "code.gitea.io/gitea/models/issues"
	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/optional"
	"code.gitea.io/gitea/modules/setting"
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
	Issue             *issues_model.Issue
	Children          []*IssueNode
	Level             int
	OpenDescendants   int
	ClosedDescendants int
}

// countOpenDescendants recursively counts all open descendant issues
func (n *IssueNode) countOpenDescendants() int {
	count := 0
	for _, child := range n.Children {
		if !child.Issue.IsClosed {
			count++
		}
		count += child.countOpenDescendants()
	}
	return count
}

// countClosedDescendants recursively counts all closed descendant issues
func (n *IssueNode) countClosedDescendants() int {
	count := 0
	for _, child := range n.Children {
		if child.Issue.IsClosed {
			count++
		}
		count += child.countClosedDescendants()
	}
	return count
}

// ComputeDescendantCounts precomputes open/closed descendant counts for this node and all children
func (n *IssueNode) ComputeDescendantCounts() {
	n.OpenDescendants = n.countOpenDescendants()
	n.ClosedDescendants = n.countClosedDescendants()
	for _, child := range n.Children {
		child.ComputeDescendantCounts()
	}
}

// MatchesFilter checks if this node or any of its descendants match the filter criteria
func (n *IssueNode) MatchesFilter(state string, labelIDs []int64, assigneeID, milestoneID int64) bool {
	// Check if this node matches
	if nodeMatchesFilter(n.Issue, state, labelIDs, assigneeID, milestoneID) {
		return true
	}
	// Check if any descendant matches
	for _, child := range n.Children {
		if child.MatchesFilter(state, labelIDs, assigneeID, milestoneID) {
			return true
		}
	}
	return false
}

// nodeMatchesFilter checks if an issue matches the filter criteria
func nodeMatchesFilter(issue *issues_model.Issue, state string, labelIDs []int64, assigneeID, milestoneID int64) bool {
	// Check state filter
	switch state {
	case "open":
		if issue.IsClosed {
			return false
		}
	case "closed":
		if !issue.IsClosed {
			return false
		}
		// "all" or empty: no state filtering
	}

	// If no other filters, match
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

// FilterTree filters the tree to only include nodes that match the filter or have matching descendants.
// It preserves precomputed descendant counts from the original tree.
func (n *IssueNode) FilterTree(state string, labelIDs []int64, assigneeID, milestoneID int64) *IssueNode {
	if !n.MatchesFilter(state, labelIDs, assigneeID, milestoneID) {
		return nil
	}

	// Create new node with filtered children, preserving precomputed counts
	filteredNode := &IssueNode{
		Issue:             n.Issue,
		Level:             n.Level,
		OpenDescendants:   n.OpenDescendants,
		ClosedDescendants: n.ClosedDescendants,
	}

	for _, child := range n.Children {
		if filteredChild := child.FilterTree(state, labelIDs, assigneeID, milestoneID); filteredChild != nil {
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
	state := ctx.FormString("state")
	if state == "" {
		state = "open"
	}
	ctx.Data["State"] = state

	page := ctx.FormInt("page")
	if page <= 0 {
		page = 1
	}

	// Sort type
	sortType := ctx.FormString("sort")
	if sortType == "" {
		sortType = "latest"
	}
	ctx.Data["SortType"] = sortType

	// View type (all, assigned, created_by, mentioned)
	viewType := ctx.FormString("type")
	validTypes := []string{"all", "assigned", "created_by", "mentioned"}
	if !isValidViewType(viewType, validTypes) {
		viewType = "all"
	}
	ctx.Data["ViewType"] = viewType

	// Keyword search
	keyword := strings.TrimSpace(ctx.FormString("q"))
	ctx.Data["Keyword"] = keyword

	// Poster filter
	posterUsername := ctx.FormString("poster")
	ctx.Data["PosterUsername"] = posterUsername

	milestoneID := ctx.FormInt64("milestone")
	assigneeID := ctx.FormString("assignee")

	// Get label filter
	labelFilter := issue.PrepareFilterIssueLabels(ctx, ctx.Repo.Repository.ID, ctx.Repo.Owner)
	if ctx.Written() {
		return
	}

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

	// Apply view type overrides when signed in
	if ctx.IsSigned {
		switch viewType {
		case "created_by":
			// will be handled below
		case "mentioned":
			// will be handled below
		case "assigned":
			assigneeID = strconv.FormatInt(ctx.Doer.ID, 10)
			assigneeIDInt = ctx.Doer.ID
		}
	}

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

	// Build issue options - fetch ALL issues (open+closed) so the tree can be built
	// State filtering is applied at the tree level, same as labels
	issueOpts := &issues_model.IssuesOptions{
		RepoIDs:  []int64{ctx.Repo.Repository.ID},
		IsPull:   optional.Some(false),
		SortType: sortType,
		Paginator: &db.ListOptions{
			Page:     page,
			PageSize: setting.UI.IssuePagingNum,
		},
	}

	// Apply view type filters to issue options
	if ctx.IsSigned {
		switch viewType {
		case "created_by":
			issueOpts.PosterID = strconv.FormatInt(ctx.Doer.ID, 10)
		case "mentioned":
			issueOpts.MentionedID = ctx.Doer.ID
		case "assigned":
			issueOpts.AssigneeID = assigneeID
		}
	}

	// Keyword is passed through for the search form display
	// Full-text search requires the issue indexer which is handled separately

	// Get issues
	issues, err := issues_model.Issues(ctx, issueOpts)
	if err != nil {
		ctx.ServerError("Issues", err)
		return
	}

	// Batch-load only the attributes needed for the backlog view
	issueList := issues
	if _, err := issueList.LoadRepositories(ctx); err != nil {
		ctx.ServerError("LoadRepositories", err)
		return
	}
	if err := issueList.LoadPosters(ctx); err != nil {
		ctx.ServerError("LoadPosters", err)
		return
	}
	if err := issueList.LoadLabels(ctx); err != nil {
		ctx.ServerError("LoadLabels", err)
		return
	}
	if err := issueList.LoadMilestones(ctx); err != nil {
		ctx.ServerError("LoadMilestones", err)
		return
	}
	if err := issueList.LoadAssignees(ctx); err != nil {
		ctx.ServerError("LoadAssignees", err)
		return
	}

	// Build issue map for quick lookup
	issueMap := make(map[int64]*issues_model.Issue)
	for _, issue := range issues {
		issueMap[issue.ID] = issue
	}

	// Pre-fetch all dependencies in a single query to avoid N+1 queries
	// depsByIssueID maps issue_id -> list of dependency_ids (issues it depends on / is blocked by)
	issueIDs := make([]int64, 0, len(issues))
	for _, issue := range issues {
		issueIDs = append(issueIDs, issue.ID)
	}

	depsByIssueID := make(map[int64][]int64)
	if len(issueIDs) > 0 {
		var deps []issues_model.IssueDependency
		err = db.GetEngine(ctx).
			In("issue_id", issueIDs).
			Find(&deps)
		if err != nil {
			ctx.ServerError("FindDependencies", err)
			return
		}
		for _, dep := range deps {
			depsByIssueID[dep.IssueID] = append(depsByIssueID[dep.IssueID], dep.DependencyID)
		}
	}

	// Find root issues (issues that are NOT dependencies of other issues)
	// If issue A depends on issue B, then A is parent (root) and B is child
	isChild := make(map[int64]bool)
	for _, depIDs := range depsByIssueID {
		for _, depID := range depIDs {
			if _, exists := issueMap[depID]; exists {
				isChild[depID] = true
			}
		}
	}

	// Root issues are those not marked as children
	rootIssues := make([]*issues_model.Issue, 0)
	for _, issue := range issues {
		if !isChild[issue.ID] {
			rootIssues = append(rootIssues, issue)
		}
	}

	// Build tree recursively using pre-fetched dependencies
	tree := make([]*IssueNode, 0)
	for _, issue := range rootIssues {
		node := buildIssueTree(issue, issueMap, depsByIssueID, make(map[int64]bool), 0)
		if node != nil {
			tree = append(tree, node)
		}
	}

	// Precompute descendant counts on the full tree before any filtering
	for _, node := range tree {
		node.ComputeDescendantCounts()
	}

	// Filter tree based on state, milestone, label and assignee filters
	// State is filtered at the tree level (like labels) to preserve tree hierarchy
	filteredTree := make([]*IssueNode, 0)
	for _, node := range tree {
		if filteredNode := node.FilterTree(state, labelFilter.SelectedLabelIDs, assigneeIDInt, milestoneID); filteredNode != nil {
			filteredTree = append(filteredTree, filteredNode)
		}
	}
	tree = filteredTree

	ctx.Data["IssueTree"] = tree

	// Open/Closed counts for the tab switcher — apply active filters so counts are accurate
	countOpts := &issues_model.IssuesOptions{
		RepoIDs: []int64{ctx.Repo.Repository.ID},
		IsPull:  optional.Some(false),
	}
	if len(labelFilter.SelectedLabelIDs) > 0 {
		countOpts.LabelIDs = labelFilter.SelectedLabelIDs
	}
	if milestoneID != 0 {
		if milestoneID == -1 {
			countOpts.MilestoneIDs = []int64{db.NoConditionID}
		} else {
			countOpts.MilestoneIDs = []int64{milestoneID}
		}
	}
	if assigneeIDInt != 0 {
		countOpts.AssigneeID = assigneeID
	}
	if ctx.IsSigned {
		switch viewType {
		case "created_by":
			countOpts.PosterID = strconv.FormatInt(ctx.Doer.ID, 10)
		case "mentioned":
			countOpts.MentionedID = ctx.Doer.ID
		case "assigned":
			countOpts.AssigneeID = strconv.FormatInt(ctx.Doer.ID, 10)
		}
	}

	openCountOpts := *countOpts
	openCountOpts.IsClosed = optional.Some(false)
	openCount, err := issues_model.CountIssues(ctx, &openCountOpts)
	if err != nil {
		log.Error("CountIssues for open: %v", err)
	}
	closedCountOpts := *countOpts
	closedCountOpts.IsClosed = optional.Some(true)
	closedCount, err := issues_model.CountIssues(ctx, &closedCountOpts)
	if err != nil {
		log.Error("CountIssues for closed: %v", err)
	}
	ctx.Data["OpenCount"] = openCount
	ctx.Data["ClosedCount"] = closedCount

	// Pagination
	totalCount := openCount + closedCount
	switch state {
	case "open":
		totalCount = openCount
	case "closed":
		totalCount = closedCount
	}

	pager := context.NewPagination(int(totalCount), setting.UI.IssuePagingNum, page, 5)
	pager.AddParamFromRequest(ctx.Req)
	ctx.Data["Page"] = pager

	ctx.HTML(http.StatusOK, tplBacklog)
}

// buildIssueTree recursively builds a tree where parents depend on children
// An issue that depends on others is the parent, the dependencies are children
func buildIssueTree(issue *issues_model.Issue, issueMap map[int64]*issues_model.Issue, depsByIssueID map[int64][]int64, visited map[int64]bool, level int) *IssueNode {
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

	// Use pre-fetched dependency map instead of per-node DB queries
	for _, depID := range depsByIssueID[issue.ID] {
		if childIssue, exists := issueMap[depID]; exists {
			// Copy the visited map for each child so that siblings in a DAG
			// (issues depended on by multiple parents) can each appear in the tree.
			childVisited := make(map[int64]bool, len(visited))
			maps.Copy(childVisited, visited)

			childNode := buildIssueTree(childIssue, issueMap, depsByIssueID, childVisited, level+1)
			if childNode != nil {
				node.Children = append(node.Children, childNode)
			}
		}
	}

	return node
}

// isValidViewType checks if the given viewType is in the list of valid types
func isValidViewType(viewType string, validTypes []string) bool {
	return slices.Contains(validTypes, viewType)
}
