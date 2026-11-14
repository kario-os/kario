Page("index", {
  title: phone.t("Counter"),
  data: { n: 0 },
  onLoad: function () {
    this.data.n = phone.getStorage("n") || 0;
  },
  render: function () {
    return [
      ui.text(phone.t("Clicked %d times", this.data.n)),
      ui.button("inc", phone.t("Add one")),
    ];
  },
  onAction: function (e) {
    if (e.action === "inc") {
      var n = this.data.n + 1;
      phone.setStorage("n", n);
      this.setData({ n: n });
    }
  },
});
