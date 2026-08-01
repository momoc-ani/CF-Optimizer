import type { ReactNode } from 'react';
import { flexRender, getCoreRowModel, getSortedRowModel, useReactTable, type ColumnDef, type SortingState } from '@tanstack/react-table';
import { ScrollArea, Table, Text } from '@mantine/core';
import { ArrowDown, ArrowUp, ChevronsUpDown } from 'lucide-react';
import { useState } from 'react';
import { EmptyState } from './Page';

interface DataTableProps<T> {
  columns: ColumnDef<T, any>[];
  data: T[];
  emptyTitle?: string;
  emptyDetail?: string;
  minWidth?: number;
  rowKey?: (row: T, index: number) => string;
  onRowClick?: (row: T) => void;
  footer?: ReactNode;
}

export function DataTable<T>({ columns, data, emptyTitle = '暂无数据', emptyDetail = '后台尚未返回可显示的记录。', minWidth = 760, rowKey, onRowClick, footer }: DataTableProps<T>) {
  const [sorting, setSorting] = useState<SortingState>([]);
  const table = useReactTable({ data, columns, state: { sorting }, onSortingChange: setSorting, getCoreRowModel: getCoreRowModel(), getSortedRowModel: getSortedRowModel() });
  if (data.length === 0) return <EmptyState title={emptyTitle} detail={emptyDetail} />;
  return (
    <ScrollArea type="auto" className="table-scroll">
      <Table striped highlightOnHover withRowBorders verticalSpacing="xs" miw={minWidth} className="data-table">
        <Table.Thead>
          {table.getHeaderGroups().map((headerGroup) => (
            <Table.Tr key={headerGroup.id}>
              {headerGroup.headers.map((header) => {
                const sorted = header.column.getIsSorted();
                const Icon = sorted === 'asc' ? ArrowUp : sorted === 'desc' ? ArrowDown : ChevronsUpDown;
                return (
                  <Table.Th key={header.id} style={{ width: header.getSize() }}>
                    <button className="table-sort" disabled={!header.column.getCanSort()} onClick={header.column.getToggleSortingHandler()}>
                      {flexRender(header.column.columnDef.header, header.getContext())}
                      {header.column.getCanSort() && <Icon size={13} />}
                    </button>
                  </Table.Th>
                );
              })}
            </Table.Tr>
          ))}
        </Table.Thead>
        <Table.Tbody>
          {table.getRowModel().rows.map((row, index) => (
            <Table.Tr key={rowKey ? rowKey(row.original, index) : row.id} onClick={() => onRowClick?.(row.original)} className={onRowClick ? 'clickable-row' : undefined}>
              {row.getVisibleCells().map((cell) => <Table.Td key={cell.id}>{flexRender(cell.column.columnDef.cell, cell.getContext())}</Table.Td>)}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      {footer && <Text component="div" size="xs" c="dimmed" mt="xs">{footer}</Text>}
    </ScrollArea>
  );
}
