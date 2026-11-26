// Written in TypeScript: the host strips types at load time (esbuild Go API);
// `tsc -p ..` checks them against types/phone.d.ts.

interface MenuItem {
  id: string;
  name: string;
  price: number;
}

type Cart = Record<string, number>;

const MENU: MenuItem[] = [
  { id: "latte", name: phone.t("Latte"), price: 1800 },
  { id: "americano", name: phone.t("Americano"), price: 1500 },
  { id: "croissant", name: phone.t("Croissant"), price: 1200 },
];

function yuan(cents: number): string {
  return "¥" + (cents / 100).toFixed(2);
}

function total(cart: Cart): number {
  return MENU.reduce((sum, m) => sum + (cart[m.id] || 0) * m.price, 0);
}

Page("index", {
  title: phone.t("Morning Light Coffee"),
  render() {
    return [
      ui.text(phone.t("Welcome"), "big"),
      ui.text(phone.t("Scan the QR code on the table to order"), "dim"),
      ui.button("scan", phone.t("Scan to order")),
    ];
  },
  onAction(e) {
    if (e.action !== "scan") return;
    phone.scanCode({
      success(res) {
        const [, table, merchant] = res.result.split("|");
        phone.navigateTo("menu", { merchant, table });
      },
      fail(err) {
        phone.toast(phone.t("Scan failed: %s", err.errMsg));
      },
    });
  },
});

Page<{ cart: Cart; merchant: string }>("menu", {
  title: phone.t("Order"),
  data: { cart: {}, merchant: "" },
  onLoad(q) {
    this.data.merchant = String(q.merchant);
  },
  render() {
    const { cart, merchant } = this.data;
    const body: UiNode[] = [ui.section(phone.t("%s · Menu", merchant))];
    for (const m of MENU) {
      const n = cart[m.id] || 0;
      body.push(ui.item({
        id: m.id, title: m.name, subtitle: yuan(m.price), trailing: n ? "×" + n : "",
        actions: [ui.primary("add", phone.t("Add one")), ui.action("sub", phone.t("Remove one"), "-")],
      }));
    }
    body.push(ui.text(phone.t("Total %s", yuan(total(cart))), "big"));
    body.push(ui.button("pay", phone.t("Pay")));
    return body;
  },
  onAction(e) {
    const cart: Cart = { ...this.data.cart };
    if (e.action === "add") cart[e.node] = (cart[e.node] || 0) + 1;
    if (e.action === "sub" && cart[e.node]) cart[e.node] -= 1;
    if (e.action === "pay") {
      const amount = total(cart);
      if (!amount) {
        phone.toast(phone.t("Order something first"));
        return;
      }
      phone.requestPayment({
        merchant: this.data.merchant,
        amount,
        success: (r) => phone.navigateTo("done", { amount, order: r.orderId }),
        fail: (err) => phone.toast(phone.t("Payment not completed: %s", err.errMsg)),
      });
      return;
    }
    this.setData({ cart });
  },
});

Page<{ amount: number; order: string }>("done", {
  title: phone.t("Payment successful"),
  data: { amount: 0, order: "" },
  onLoad(q) {
    this.data.amount = Number(q.amount);
    this.data.order = String(q.order);
  },
  render() {
    return [
      ui.text("✓ " + phone.t("Payment successful"), "big"),
      ui.text(yuan(this.data.amount), "big"),
      ui.text(phone.t("Order number %s", this.data.order), "dim"),
      ui.text(phone.t("Host server signature check: passed"), "ok"),
      ui.button("finish", phone.t("Done")),
    ];
  },
  onAction() {
    phone.navigateBack(2);
  },
});
