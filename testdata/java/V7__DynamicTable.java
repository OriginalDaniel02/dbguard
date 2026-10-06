package db.migration;

import java.sql.Statement;
import org.flywaydb.core.api.migration.BaseJavaMigration;
import org.flywaydb.core.api.migration.Context;

public class V7__DynamicTable extends BaseJavaMigration {
    @Override
    public void migrate(Context context) throws Exception {
        String table = System.getProperty("ledger.table", "transactions");
        try (Statement st = context.getConnection().createStatement()) {
            st.execute(String.format("CREATE INDEX idx_%s_ref ON %s (ref)", table, table));
            st.execute("ALTER TABLE " + table + " ADD CONSTRAINT chk_pos CHECK (amount > 0)");
        }
    }
}
