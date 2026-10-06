package db.migration;

import java.sql.Statement;
import org.flywaydb.core.api.migration.BaseJavaMigration;
import org.flywaydb.core.api.migration.Context;

public class V5__Acknowledged extends BaseJavaMigration {
    @Override
    public void migrate(Context context) throws Exception {
        try (Statement st = context.getConnection().createStatement()) {
            // dbguard:ignore create-index reason: transactions is write-quiet during the 03:00 deploy window
            st.execute("CREATE INDEX idx_transactions_ref ON transactions (ref)");
        }
    }
}
