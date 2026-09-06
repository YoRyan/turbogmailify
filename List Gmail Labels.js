// This is a Google App Script. Run it at https://script.google.com/
function listLabels() {
  const labels = Gmail.Users.Labels
    .list("me")
    .labels;
  console.log(
    "System Labels:\n\n" +
    labels
      .filter(label => label.type === "system")
      .map(label => label.id)
      .sort()
      .join("\n")
  );
  console.log(
    "User Labels:\n\n" +
    labels
      .filter(label => label.type === "user")
      .map(label => `${label.name} => ${label.id}`)
      .sort()
      .join("\n")
  );
}