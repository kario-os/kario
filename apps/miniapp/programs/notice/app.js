var NOTICE = [
  "# " + phone.t("Platform update notice"),
  "",
  phone.t("Starting this week, **mini-programs** can use *Markdown documents* rendered by the system, and Mermaid flowcharts are drawn by the system too:"),
  "",
  "```mermaid",
  "flowchart TD",
  "  A[" + phone.t("Scan") + "] --> B{" + phone.t("Merchant trusted?") + "}",
  "  B -->|" + phone.t("Yes") + "| C[" + phone.t("System security confirmation") + "]",
  "  B -->|" + phone.t("No") + "| D[" + phone.t("Warn of the risk") + "]",
  "```",
  "",
  "- " + phone.t("Headings, lists, quotes, `inline code`"),
  "- " + phone.t("Tables and code blocks"),
  "- " + phone.t("Links open from the menu, for example [platform architecture](%s) and [capability catalogue](%s)",
    "https://example.com/arch", "https://example.com/caps"),
  "",
  "> " + phone.t("Documents are drawn by the system: text in a terminal, typeset on production devices, and neither needs a browser."),
  "",
  "| " + phone.t("Renderer") + " | " + phone.t("Text") + " | " + phone.t("Images") + " |",
  "|---|---|---|",
  "| " + phone.t("Text mode") + " | " + phone.t("As is") + " | " + phone.t("Half-block pixels") + " |",
  "| " + phone.t("Pixel") + " | " + phone.t("Typeset") + " | " + phone.t("Original") + " |",
  "",
  "```js",
  "// " + phone.t("Code blocks are coloured by language"),
  "Page('index', { render() { return [ui.doc(NOTICE, 42)] } })",
  "```",
  "",
  phone.t("The instalment fee is %s, and compound interest is:", "$f = r \\times a$"),
  "",
  "$$A = P(1 + r/n)^{nt}$$",
].join("\n");

Page("index", {
  title: phone.t("Notice board"),
  render: function () {
    return [ui.doc(NOTICE, "notice")];
  },
  onAction: function (e) {
    if (e.action.indexOf("link:") === 0) {
      phone.toast(phone.t("Open link: %s", e.value));
    }
  },
});
