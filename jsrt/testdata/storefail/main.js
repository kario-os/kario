// What storage_test.go drives: a main module that stores, then fails.
import * as kario from "kario";

kario.storage.set("v", "stored");
throw new Error("failed after storing");
