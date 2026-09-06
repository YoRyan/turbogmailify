// This is a Google App Script. Run it at https://script.google.com/
function listLabels() {
  const labels = Gmail.Users.Labels
    .list("me")
    .labels;
  console.log(
    "System Labels:\n\n" +
    labels
      .filter(({ type }) => type === "system")
      .map(({ id }) => id)
      .sort()
      .join("\n")
  );
  console.log(
    "User Labels:\n\n" +
    labels
      .filter(({ type }) => type === "user")
      .map(({ name, id }) => `${name} => ${id}`)
      .sort()
      .join("\n")
  );
}