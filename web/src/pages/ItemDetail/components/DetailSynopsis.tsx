export default function DetailSynopsis({
  overview,
  translating,
  onTranslate,
}: {
  overview?: string | null;
  translating?: boolean;
  onTranslate?: () => void;
}) {
  if (!overview) return null;
  return (
    <section className="detail-mobile-synopsis">
      <h2 className="mb-3 text-xl font-semibold">Synopsis</h2>
      <p className="text-muted-foreground max-w-2xl leading-relaxed">{overview}</p>
      {translating ? (
        <p role="status">Translating…</p>
      ) : (
        onTranslate && (
          <button
            type="button"
            onClick={onTranslate}
            className="mt-3 rounded-full border px-3 py-2 text-sm"
          >
            Translate
          </button>
        )
      )}
    </section>
  );
}
