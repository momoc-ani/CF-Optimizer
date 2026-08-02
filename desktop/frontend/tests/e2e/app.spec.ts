import { expect, test } from '@playwright/test';

const pages = ['总览', '测速优选', '域名加速', '代理适配', '网络路由', '网段管理', '历史记录', '日志诊断', '设置'];

test('九个核心页面可导航且保持在工作区内', async ({ page }, testInfo) => {
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
  await page.getByRole('button', { name: '取消当前任务' }).click();
  await expect(page.getByText('任务已取消').first()).toBeVisible();
});

test('域名加速页面展示完整的直连验证证据', async ({ page }, testInfo) => {
  await page.goto('/');
  await page.getByRole('button', { name: '域名加速', exact: true }).click();
  await expect(page.getByRole('heading', { name: '域名加速', exact: true })).toBeVisible();
  await expect(page.getByText('加速策略', { exact: true })).toBeVisible();
  await expect(page.getByLabel('启用 Cloudflare 域名加速')).toBeChecked();
  await expect(page.getByText('ani.momoc.top').first()).toBeVisible();
  await expect(page.getByText('Mihomo DIRECT').first()).toBeVisible();
  await expect(page.getByText('HTTPS 与物理直连证据完整')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('acceleration-page.png'), fullPage: false });
});

test('域名加速配置只在独立页面编辑', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: '域名加速', exact: true }).click();
  await page.getByLabel('排除域名').fill('skip.example.com');
  await page.getByRole('button', { name: '保存策略' }).click();
  await expect(page.getByText('加速策略已保存')).toBeVisible();

  await page.getByRole('button', { name: '设置', exact: true }).click();
  await expect(page.getByRole('heading', { name: '设置', exact: true })).toBeVisible();
  await expect(page.getByText('加速域名', { exact: true })).toHaveCount(0);
  await expect(page.getByLabel('启用 Cloudflare 域名加速')).toHaveCount(0);
});

test('设置页展示开源许可和 GitHub 来源', async ({ page }, testInfo) => {
  await page.goto('/');
  await page.getByRole('button', { name: '设置', exact: true }).click();
  await expect(page.getByText('MIT License', { exact: false })).toBeVisible();
  await expect(page.getByText('Copyright (c) 2026 CF Optimizer Contributors')).toBeVisible();
  const sourceButton = page.getByRole('button', { name: '打开 GitHub 源码仓库' });
  await expect(sourceButton).toBeVisible();
  await sourceButton.hover();
  await expect(page.getByText('在 GitHub 打开源码仓库')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('open-source-about.png'), fullPage: false });
});

test('一键优选先展示影响确认再执行', async ({ page }) => {
  await page.goto('/');
  await page.getByRole('button', { name: '一键优选' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText('Ethernet 2')).toBeVisible();
  await expect(dialog.getByText('系统主机路由')).toBeVisible();
  await dialog.getByText('以后自动维护').click();
  await dialog.getByRole('button', { name: '开始并验证' }).click();
  await expect(page.getByRole('status')).toBeVisible();
  await expect(page.getByText('已验证').first()).toBeVisible({ timeout: 5_000 });
});

test('计划过期后重新生成确认计划', async ({ page }) => {
  await page.goto('/?quickstart=expired');
  await page.getByRole('button', { name: '一键优选' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByRole('button', { name: '开始并验证' }).click();
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText('物理出口已完成只读预检')).toBeVisible();
});
