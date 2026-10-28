import licensesText from "../../THIRD_PARTY_LICENSES.txt?raw";
import { extPlugin } from "../../extPlugins";
import styles from "./Licenses.module.css";

export function Licenses() {
  const appendix = extPlugin.thirdPartyLicensesAppendix?.trim();
  const body = appendix ? `${licensesText}\n\n${appendix}\n` : licensesText;
  return (
    <div className={styles.container}>
      <h1 className={styles.title}>Third-Party Licenses</h1>
      <pre className={styles.body}>{body}</pre>
    </div>
  );
}
