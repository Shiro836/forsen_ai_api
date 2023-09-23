function updateWhitelist() {
    var xmlHttp = new XMLHttpRequest();
    xmlHttp.open("GET", "/get_whitelist", false);
    xmlHttp.send(null);

    document.getElementById("whitelist").innerHTML = ""

    const res = JSON.parse(xmlHttp.responseText)["list"];

    var html = ["<table style='border: 1px solid black;'>"]

    function th(str) {
        return "<th style='border: 1px solid black;'>" + str + "</th>"
    }

    html.push("<tr>", th("Login"), th("Is Mod"), th("Added By"), th("Banned By"), "</tr>")

    function td(str, clr=null) {
        return "<td style='border: 1px solid black;'>" + str + "</td>"
    }

    for (var i=0;i<res.length;i++) {
        clr = "white"

        if (res[i]["is_mod"]) {
            clr = "lime"
        } else if (res[i]["banned_by"] !== null) {
            clr = "#FFCCCB"
        }

        html.push("<tr bgcolor=" + clr + ">")

        login = res[i]["login"]
        is_banned = (res[i]["banned_by"] !== null)

        html.push(td(is_banned ? login : "<a href='https://twitch.tv/" + login + "' target='_blank'>" + login + "</a>"))
        html.push(td(res[i]["is_mod"]))
        html.push(td(res[i]["added_by"]))
        html.push(td(is_banned ? res[i]["banned_by"] : ""))

        console.log(res[i])
        html.push("</tr>")
    }

    html.push("</table>")

    document.getElementById("whitelist").innerHTML = html.join("")
}
