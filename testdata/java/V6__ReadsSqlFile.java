package db.migration;

import java.io.InputStream;
import java.sql.Statement;
import org.flywaydb.core.api.migration.BaseJavaMigration;
import org.flywaydb.core.api.migration.Context;

public class V6__ReadsSqlFile extends BaseJavaMigration {
    @Override
    public void migrate(Context context) throws Exception {
        try (InputStream in = getClass().getResourceAsStream("/db/ddl/v6.sql");
             Statement st = context.getConnection().createStatement()) {
            st.execute(new String(in.readAllBytes()));
        }
    }
}
