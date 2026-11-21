// Previews (kario preview): the page drawn with sample state on each form
// factor, without the app running. kario build leaves this file out.
import { preview } from "@kario/ui";
import { Main, count } from "./main";

preview("new", Main, { state: () => count.set(0) });
preview("tapped", Main, { state: () => count.set(42) });
