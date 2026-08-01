import { createTheme, rem } from '@mantine/core';

export const theme = createTheme({
  primaryColor: 'ocean',
  primaryShade: { light: 7, dark: 5 },
  colors: {
    ocean: ['#e7f6fb', '#d2edf7', '#a8dced', '#7ac9e2', '#55b8d8', '#3cadd2', '#269fc5', '#1677a6', '#176588', '#18546f'],
  },
  fontFamily: '-apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans SC", sans-serif',
  fontFamilyMonospace: '"SFMono-Regular", Consolas, "Liberation Mono", monospace',
  defaultRadius: 6,
  radius: { xs: rem(3), sm: rem(5), md: rem(6), lg: rem(8), xl: rem(8) },
  spacing: { xs: rem(8), sm: rem(12), md: rem(16), lg: rem(20), xl: rem(24) },
  headings: { fontFamily: 'inherit', fontWeight: '650' },
  components: {
    Button: { defaultProps: { size: 'sm' } },
    ActionIcon: { defaultProps: { variant: 'subtle' } },
    TextInput: { defaultProps: { size: 'sm' } },
    NumberInput: { defaultProps: { size: 'sm' } },
    Select: { defaultProps: { size: 'sm' } },
  },
});
