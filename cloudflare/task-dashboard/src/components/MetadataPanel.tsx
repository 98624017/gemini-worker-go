interface MetadataPanelProps {
  metadata: Record<string, unknown>;
}

export function MetadataPanel({ metadata }: MetadataPanelProps) {
  const entries = Object.entries(metadata);
  if (entries.length === 0) return null;

  return (
    <div className="collapse collapse-arrow bg-base-200 rounded-lg">
      <input type="checkbox" />
      <div className="collapse-title text-sm font-medium">
        Usage Metadata
      </div>
      <div className="collapse-content">
        <div className="overflow-x-auto">
          <table className="table table-xs">
            <tbody>
              {entries.map(([key, value]) => (
                <tr key={key}>
                  <td className="font-mono text-xs text-base-content/60 whitespace-nowrap">
                    {key}
                  </td>
                  <td className="text-xs">
                    {typeof value === "object"
                      ? JSON.stringify(value)
                      : String(value)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
