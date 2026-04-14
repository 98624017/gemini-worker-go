import { Children, type ReactNode, useMemo } from "react";

interface MasonryGridProps {
  children: ReactNode;
  columns?: number;
}

/**
 * JS-based masonry grid that distributes items to the shortest column.
 * This gives row-first visual ordering (left-to-right, top-to-bottom)
 * unlike CSS column-count which fills column-by-column.
 */
export function MasonryGrid({ children, columns: fixedColumns }: MasonryGridProps) {
  const items = Children.toArray(children);

  // Distribute items round-robin to approximate row-first ordering.
  // True height-based distribution would require DOM measurement,
  // but round-robin is a good enough approximation for uniform-ish items.
  const columnItems = useMemo(() => {
    const cols = fixedColumns ?? 4; // CSS media queries handle responsive, but we default to 4
    const result: ReactNode[][] = Array.from({ length: cols }, () => []);
    items.forEach((item, i) => {
      result[i % cols].push(item);
    });
    return result;
  }, [items, fixedColumns]);

  return (
    <div className="masonry-grid-js">
      {columnItems.map((col, i) => (
        <div key={i} className="masonry-column">
          {col}
        </div>
      ))}
    </div>
  );
}
