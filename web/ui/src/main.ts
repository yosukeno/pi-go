import { createApp } from "vue";
import ElementPlus from "element-plus";
import "element-plus/dist/index.css";
import "./styles.scss";
import App from "./App.vue";
import { i18n } from "./i18n";

// Element Plus is imported whole rather than on demand: this binary is served
// from localhost, so a smaller bundle is worth less than fewer moving parts. Its
// CSS variables are also what the components style against.
createApp(App).use(i18n).use(ElementPlus).mount("#app");
