package db.migration;

import java.sql.Statement;
import org.flywaydb.core.api.migration.BaseJavaMigration;
import org.flywaydb.core.api.migration.Context;

public class V4__Mixed extends BaseJavaMigration {
    private static final String ADD_NOTE = "ALTER TABLE accounts " +
            "ADD COLUMN note text";

    @Override
    public void migrate(Context context) throws Exception {
        try (Statement st = context.getConnection().createStatement()) {
            // st.execute("CREATE INDEX never_runs ON transactions (a)");
            System.out.println("Alter table failed, will retry");
            st.execute(ADD_NOTE);
            st.execute("""
                CREATE TABLE ledger_archive (id bigint, amount numeric);

                CREATE INDEX idx_archive_amount ON ledger_archive (amount);
                """);
            st.execute("ALTER TABLE transactions ADD COLUMN created_at timestamptz DEFAULT clock_timestamp()");
        }
    }
}
