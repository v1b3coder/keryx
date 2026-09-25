package cz.v1b3coder.keryx;

import static org.junit.Assert.assertEquals;

import java.util.Arrays;
import java.util.Collections;
import java.util.HashSet;
import java.util.List;

import org.junit.Test;

public class KeryxTopicsTest {
    @Test
    public void addsOnlyNewTopics() {
        assertEquals(List.of("b"),
                KeryxTopics.added(new HashSet<>(Arrays.asList("a")), new HashSet<>(Arrays.asList("a", "b"))));
    }

    @Test
    public void removesOnlyDroppedTopics() {
        assertEquals(List.of("a"),
                KeryxTopics.removed(new HashSet<>(Arrays.asList("a", "b")), new HashSet<>(Arrays.asList("b"))));
    }

    @Test
    public void reportsNothingWhenTheSetsMatch() {
        assertEquals(Collections.emptyList(),
                KeryxTopics.added(new HashSet<>(Arrays.asList("a")), new HashSet<>(Arrays.asList("a"))));
        assertEquals(Collections.emptyList(),
                KeryxTopics.removed(new HashSet<>(Arrays.asList("a")), new HashSet<>(Arrays.asList("a"))));
    }
}
