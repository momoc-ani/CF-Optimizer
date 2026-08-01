import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Alert, Button, Stack, Text, Title } from '@mantine/core';
import { CircleAlert, RotateCcw } from 'lucide-react';

interface Props { children: ReactNode }
interface State { error?: Error }

export class ErrorBoundary extends Component<Props, State> {
  state: State = {};

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, details: ErrorInfo) {
    console.error('UI render failed', error, details.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <main className="fatal-state">
        <Stack maw={560} gap="md">
          <CircleAlert size={36} color="var(--mantine-color-red-7)" />
          <Title order={2}>界面暂时无法显示</Title>
          <Text c="dimmed">后台服务不会因此停止。重新加载界面后可继续查看任务状态。</Text>
          <Alert color="red" title="渲染错误">{this.state.error.message}</Alert>
          <Button leftSection={<RotateCcw size={16} />} onClick={() => window.location.reload()}>重新加载</Button>
        </Stack>
      </main>
    );
  }
}
