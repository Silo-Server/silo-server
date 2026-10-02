interface BulkSelectionCheckboxProps {
  label: string;
  selected: boolean;
  onSelectionChange: (checked: boolean, extendRange: boolean) => void;
}

export function BulkSelectionCheckbox({
  label,
  selected,
  onSelectionChange,
}: BulkSelectionCheckboxProps) {
  return (
    <input
      type="checkbox"
      className="accent-primary size-4 shrink-0 cursor-pointer"
      checked={selected}
      aria-label={label}
      onClick={(event) => event.stopPropagation()}
      onChange={(event) => {
        const shiftKey = event.nativeEvent instanceof MouseEvent && event.nativeEvent.shiftKey;
        onSelectionChange(event.target.checked, shiftKey);
      }}
    />
  );
}
