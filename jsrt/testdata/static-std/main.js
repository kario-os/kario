// A static import of QuickJS's std module: the app must not start.
import * as std from "std";
import * as kario from "kario";

kario.on("start", () => kario.reset({ id: "main", title: std.getenv("HOME"), body: [] }));
