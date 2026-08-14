import { createApp } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
// Element's dark variables, scoped to html.dark by the framework, so a light skin
// never sees them. Imported even though src/theme sets the same variables inline:
// this file also carries the couple of dozen tokens no skin declares (the mask
// ramps, the overlay tints), and on a dark skin those are better off dark than
// left at their light defaults. Inline properties still win wherever both exist.
import "element-plus/theme-chalk/dark/css-vars.css";
import "./styles.scss";
import App from "./App.vue";
import { i18n } from "./i18n";
import { initTheme } from "./theme";

// The skin is written onto <html> before the app mounts, so the first frame is
// already in the right colours — applying it from a component's onMounted would
// paint the fallback skin once and then repaint, which on a dark skin is a white
// flash in a dark room.
initTheme();

// Element Plus is imported whole rather than on demand: this binary is served
// from localhost, so a smaller bundle is worth less than fewer moving parts. Its
// CSS variables are also what the components style against.
createApp(App).use(i18n).use(ElementPlus).mount("#app");
