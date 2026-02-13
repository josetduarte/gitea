import {localUserSettings} from '../modules/user-settings.ts';

const STORAGE_KEY = 'backlog-collapsed';

export function initRepoBacklog(): void {
  const issueTree = document.querySelector('.issue-tree');
  if (!issueTree) return;

  // Load collapsed state from user settings
  const collapsedIssues: Set<string> = new Set(
    localUserSettings.getJsonObject<Record<string, string[]>>(STORAGE_KEY, {ids: []}).ids ?? [],
  );

  // Apply initial collapsed state
  for (const issueId of collapsedIssues) {
    const toggle = issueTree.querySelector<HTMLElement>(`.issue-toggle[data-issue-id="${issueId}"]`);
    const children = issueTree.querySelector<HTMLElement>(`.issue-children[data-parent-id="${issueId}"]`);
    if (toggle && children) {
      toggle.classList.add('collapsed');
      children.classList.add('collapsed');
    }
  }

  function saveCollapsedState(): void {
    localUserSettings.setJsonObject(STORAGE_KEY, {ids: [...collapsedIssues]});
  }

  // Handle toggle clicks via event delegation
  issueTree.addEventListener('click', (e: Event) => {
    const toggle = (e.target as HTMLElement).closest<HTMLElement>('.issue-toggle');
    if (!toggle) return;

    e.preventDefault();
    e.stopPropagation();

    const issueId = toggle.dataset.issueId;
    if (!issueId) return;

    const children = issueTree.querySelector<HTMLElement>(`.issue-children[data-parent-id="${issueId}"]`);
    if (!children) return;

    const isCollapsed = toggle.classList.contains('collapsed');
    if (isCollapsed) {
      toggle.classList.remove('collapsed');
      children.classList.remove('collapsed');
      collapsedIssues.delete(issueId);
    } else {
      toggle.classList.add('collapsed');
      children.classList.add('collapsed');
      collapsedIssues.add(issueId);
    }
    saveCollapsedState();
  });

  // Expand/Collapse all buttons (in toolbar, outside .issue-tree)
  const expandAllBtn = document.querySelector<HTMLElement>('#backlog-expand-all');
  const collapseAllBtn = document.querySelector<HTMLElement>('#backlog-collapse-all');

  expandAllBtn?.addEventListener('click', () => {
    for (const toggle of issueTree.querySelectorAll<HTMLElement>('.issue-toggle')) {
      const issueId = toggle.dataset.issueId;
      if (!issueId) continue;
      const children = issueTree.querySelector<HTMLElement>(`.issue-children[data-parent-id="${issueId}"]`);
      if (children) {
        toggle.classList.remove('collapsed');
        children.classList.remove('collapsed');
        collapsedIssues.delete(issueId);
      }
    }
    saveCollapsedState();
  });

  collapseAllBtn?.addEventListener('click', () => {
    for (const toggle of issueTree.querySelectorAll<HTMLElement>('.issue-toggle')) {
      const issueId = toggle.dataset.issueId;
      if (!issueId) continue;
      const children = issueTree.querySelector<HTMLElement>(`.issue-children[data-parent-id="${issueId}"]`);
      if (children) {
        toggle.classList.add('collapsed');
        children.classList.add('collapsed');
        collapsedIssues.add(issueId);
      }
    }
    saveCollapsedState();
  });
}
