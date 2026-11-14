// A static import of QuickJS's os module: the app must not start.
import * as os from "os";
import * as kario from "kario";

kario.on("start", () => kario.reset({ id: "main", title: String(os.getpid()), body: [] }));
