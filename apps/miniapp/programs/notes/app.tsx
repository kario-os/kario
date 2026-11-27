// The notes mini-program, written in TSX: the view is phone.Ui nodes built
// by h() (prelude.js), not DOM.
Page("index", {
  title: phone.t("Notes"),
  data: { notes: [] as string[] },
  onLoad() {
    this.data.notes = phone.getStorage<string[]>("notes") || [];
  },
  render() {
    const notes = this.data.notes as string[];
    return (
      <>
        <input id="new" placeholder={phone.t("Write a note, Enter saves")} submit="add" clear={true} />
        <section>{phone.t("%d in total", notes.length)}</section>
        {notes.map((n, i) => (
          <item id={"n" + i} title={n} actions={[ui.danger("del", phone.t("Delete"), phone.t("Delete this note?"))]} />
        ))}
      </>
    );
  },
  onAction(e) {
    const notes = (this.data.notes as string[]).slice();
    if (e.action === "add") {
      notes.unshift(e.value as string);
    } else if (e.action === "del") {
      notes.splice(parseInt(e.node.substring(1), 10), 1);
    }
    phone.setStorage("notes", notes);
    this.setData({ notes: notes });
  },
});
