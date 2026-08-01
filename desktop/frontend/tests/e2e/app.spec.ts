import { expect, test } from '@playwright/test';

const pages = ['总览', '测速优选', '代理适配', '网络路由', '网段管理', '历史记录', '日志诊断', '设置'];

test('八个核心页面可导航且保持在工作区内', async ({ page }, testInfo) => {
  await page.goto('/');
  for (const name of pages) {
    await page.getByRole('button', { name, exact: true }).click();
    await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
    const layout = await page.evaluate(() => {
      const content = document.querySelector<HTMLElement>('.workspace-content');
      const sidebar = document.querySelector<HTMLElement>('.sidebar');
      const workspace = document.querySelector<HTMLElement>('.workspace');
      if (!content || !sidebar || !workspace) return { valid: false, overflow: true, separated: false };
      const sidebarBox = sidebar.getBoundingClientRect();
      const workspaceBox = workspace.getBoundingClientRect();
      return {
        valid: content.clientWidth > 0 && content.clientHeight > 0,
        overflow: content.scrollWidth > content.clientWidth + 2,
        separated: sidebarBox.right <= workspaceBox.left + 1,
      };
    });
    expect(layout.valid).toBe(true);
    expect(layout.overflow).toBe(false);
    expect(layout.separated).toBe(true);
  }
  await page.screenshot({ path: testInfo.outputPath('core-pages-final.png'), fullPage: false });
});

test('主题切换和优选任务状态可操作', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: '切换主题' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-mantine-color-scheme', 'dark');
  await page.getByRole('button', { name: '测速优选', exact: true }).click();
  await page.getByRole('button', { name: '开始优选' }).click();
  await expect(page.getByRole('status')).toBeVisible();
  await expect(page.getByText(/TCP 初筛|下载复筛|选择节点|更新网段/).first()).toBeVisible();
});
