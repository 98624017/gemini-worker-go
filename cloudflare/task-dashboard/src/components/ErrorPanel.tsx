interface ErrorPanelProps {
  code: string;
  message: string;
  uncertain?: boolean;
}

export function ErrorPanel({ code, message, uncertain }: ErrorPanelProps) {
  return (
    <div className="alert alert-error">
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs badge badge-outline">{code}</span>
          {uncertain && (
            <span className="badge badge-warning badge-xs">transport uncertain</span>
          )}
        </div>
        <p className="text-sm">{message}</p>
      </div>
    </div>
  );
}
