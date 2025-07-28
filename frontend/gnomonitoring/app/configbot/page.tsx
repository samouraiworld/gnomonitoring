"use client";
import { useUser } from "@clerk/nextjs";
import { useEffect, useState } from "react";

type Webhook = { ID?: number; DESCRIPTION: string; URL: string; Type: string };
type WebhookType = "gov" | "val";
type WebhookField = keyof Webhook;

export default function ConfigBotPage() {
    const { user, isLoaded } = useUser();
    const [dailyHour, setDailyHour] = useState("09");
    const [dailyMinute, setDailyMinute] = useState("00");
    const [govWebhooks, setGovWebhooks] = useState<Webhook[]>([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
    const [valWebhooks, setValWebhooks] = useState<Webhook[]>([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
    const [contacts, setContacts] = useState([{ moniker: "", name: "", tag: "" }]);

    // Chargement initial des données
    useEffect(() => {
        if (!isLoaded || !user) return;

        const loadConfig = async () => {
            try {
                const res = await fetch(`/api/get-webhooks?user_id=${user.id}`);
                if (!res.ok) throw new Error("Erreur lors du chargement");

                const data = await res.json();
                console.log("✅ Data reçue du backend :", data);

                setGovWebhooks(data.govWebhooks?.length ? data.govWebhooks : [{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
                setValWebhooks(data.valWebhooks?.length ? data.valWebhooks : [{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
                setContacts(data.contacts?.length ? data.contacts : [{ moniker: "", name: "", tag: "" }]);
            } catch (err) {
                console.error("❌ Erreur lors du chargement de la configuration:", err);
            }
        };

        loadConfig();
    }, [isLoaded, user]);

    const handleWebhookChange = (type: WebhookType, index: number, field: WebhookField, value: string) => {
        const updater = type === "gov" ? setGovWebhooks : setValWebhooks;
        const current = type === "gov" ? govWebhooks : valWebhooks;

        const updated = [...current];
        updated[index] = { ...updated[index], [field]: value };
        updater(updated);
    };

    // ✅ Ajout d'un nouveau webhook (bouton Add)
    const handleAddWebhook = (type: WebhookType) => {
        if (type === "gov") {
            setGovWebhooks([...govWebhooks, { ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
        } else {
            setValWebhooks([...valWebhooks, { ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
        }
    };

    // ✅ Sauvegarde côté backend quand on clique sur "Save"
    const handleSaveNewWebhook = async (type: WebhookType, index: number) => {
        const webhook = type === "gov" ? govWebhooks[index] : valWebhooks[index];
        const target = type === "gov" ? "govdao" : "validator";

        if (!webhook.URL.trim()) {
            alert("⚠️ L'URL ne peut pas être vide !");
            return;
        }

        try {
            const res = await fetch("/api/add-webhook", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    user: user?.id,
                    description: webhook.DESCRIPTION,
                    url: webhook.URL,
                    type: webhook.Type,
                    target,
                }),
            });

            if (res.status === 409) {
                alert("⚠️ Ce webhook existe déjà !");
                return;
            }

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API:", errorText);
                alert("Erreur lors de l’enregistrement du webhook.");
            } else {
                const data = await res.json(); // supposons { id: 123 }
                alert("✅ Webhook enregistré avec succès !");

                // ✅ Mettre à jour l'ID dans le state (important pour Delete)
                if (type === "gov") {
                    const updated = [...govWebhooks];
                    updated[index].ID = data.id;
                    setGovWebhooks(updated);
                } else {
                    const updated = [...valWebhooks];
                    updated[index].ID = data.id;
                    setValWebhooks(updated);
                }
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau lors de l’appel API.");
        }
    };
    const handleUpdateNewWebhook = async (type: WebhookType, index: number) => {
        const webhook = type === "gov" ? govWebhooks[index] : valWebhooks[index];
        const target = type === "gov" ? "govdao" : "validator";

        try {
            const res = await fetch("/api/edit-webhook", {
                method: "PUT",
                headers: {
                    "Content-Type": "application/json",
                },
                body: JSON.stringify({
                    user: user?.id,
                    id: webhook.ID, // ⚠ Assure-toi que tu récupères `id` depuis l'API
                    url: webhook.URL,
                    type: webhook.Type,
                    description: webhook.DESCRIPTION,
                    target,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API Update:", errorText);
                alert("Erreur lors de la mise à jour du webhook.");
            } else {
                alert("✅ Webhook mis à jour avec succès !");
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau lors de l’appel API.");
        }
    };


    async function handleDeleteWebhook(type: WebhookType, id?: number) {
        if (!id || !user?.id) {
            alert("⚠️ Impossible de supprimer : ID ou user_id manquant");
            return;
        }

        try {
            const res = await fetch(`/api/delete-webhook`, {
                method: "POST", // car ta route Next.js utilise POST
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    id,
                    user_id: user.id,
                    type: type === "gov" ? "govdao" : "validator",
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API:", errorText);
                alert("Erreur lors de la suppression");
                return;
            }

            // ✅ Mise à jour du state local
            if (type === "gov") {
                setGovWebhooks(govWebhooks.filter(w => w.ID !== id));
            } else {
                setValWebhooks(valWebhooks.filter(w => w.ID !== id));
            }
        } catch (error) {
            console.error("❌ Erreur réseau:", error);
            alert("Erreur réseau lors de la suppression");
        }
    }

    const handleAddContact = () => {
        setContacts([...contacts, { moniker: "", name: "", tag: "" }]);
    };

    const handleContactChange = (i: number, field: string, value: string) => {
        const updated = [...contacts];
        updated[i] = { ...updated[i], [field]: value }; // FIX: maintenant ça met à jour la valeur
        setContacts(updated);
    };

    const handleDeleteContact = (i: number) => {
        const updated = [...contacts];
        updated.splice(i, 1);
        setContacts(updated);
    };

    const handleUpdateContact = async (i: number) => {
        const contact = contacts[i];
        await fetch("/api/update-contact", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ user_id: user?.id, ...contact }),
        });
        alert("Contact mis à jour");
    };

    return (
        <div className="max-w-12xl mx-auto p-4 space-y-6">
            <h1 className="text-2xl font-bold">Configurer les alertes GnoMonitoring</h1>

            {/* User Info */}
            <section>
                <h2 className="text-xl font-semibold">Utilisateur</h2>
                <p>Email : {user?.primaryEmailAddress?.emailAddress}</p>
                <div className="flex space-x-2">
                    <label>Heure du rapport quotidien :</label>
                    <input
                        type="number"
                        value={dailyHour}
                        min={0}
                        max={23}
                        onChange={(e) => setDailyHour(e.target.value)}
                        className="px-2 w-16"
                    />
                    <span>:</span>
                    <input
                        type="number"
                        value={dailyMinute}
                        min={0}
                        max={59}
                        onChange={(e) => setDailyMinute(e.target.value)}
                        className="px-2 w-16"
                    />
                </div>
            </section>

            {/* Webhooks GovDAO */}
            <section>
                <h2 className="text-xl font-semibold mb-2">Webhooks GovDAO</h2>
                <table className="w-full table-auto ">
                    <thead className="bg-black-100 text-white">
                        <tr>
                            <th className="p-2 text-center">Description</th>
                            <th className="p-2 text-center">URL</th>
                            <th className="p-2 text-center">Type</th>
                            <th className="p-2 text-center">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        {govWebhooks.map((w, i) => (
                            <tr key={i} className="text-center">
                                <td className="p-2 text-center">
                                    <input
                                        type="text"
                                        value={w.DESCRIPTION ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("gov", i, "DESCRIPTION", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2 text-center">
                                    <input
                                        type="text-area"
                                        value={w.URL ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("gov", i, "URL", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2 text-center">
                                    <select
                                        value={w.Type ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("gov", i, "Type", e.target.value)
                                        }
                                        className="border p-1"
                                    >
                                        <option value="discord">Discord</option>
                                        <option value="slack">Slack</option>
                                    </select>
                                </td>
                                <td className="p-2 text-center space-x-2">
                                    <button
                                        onClick={() => handleSaveNewWebhook("gov", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Save
                                    </button>
                                    <button
                                        onClick={() => handleUpdateNewWebhook("gov", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Update
                                    </button>
                                    <button
                                        onClick={() => handleDeleteWebhook("gov", w.ID)}
                                        disabled={!w.ID}
                                        className={`px-2 py-1 rounded text-sm ${!w.ID ? "bg-gray-400" : "bg-red-500 text-white"}`}
                                    >
                                        Delete
                                    </button>
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </table>
                <button
                    onClick={() => handleAddWebhook("gov")}
                    className="mt-2 text-blue-600 underline"
                >
                    + Ajouter un webhook GovDAO
                </button>
            </section>

            {/* Webhooks Validator */}
            <section>
                <h2 className="text-xl font-semibold mb-2">Webhooks Validator</h2>
                <table className="w-full table-auto ">
                    <thead className="bg-black-100 text-white text-center">
                        <tr>
                            <th className="p-2 text-center">DESCRIPTION</th>
                            <th className="p-2 text-center">URL</th>
                            <th className="p-2 text-center">Type</th>
                            <th className="p-2 text-center">Actions</th>
                        </tr>
                    </thead>
                    <tbody>
                        {valWebhooks.map((w, i) => (
                            <tr key={i} className="text-center">
                                <td className="p-2">
                                    <input
                                        type="text"
                                        value={w.DESCRIPTION ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("val", i, "DESCRIPTION", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2">
                                    <input
                                        type="text"
                                        value={w.URL ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("val", i, "URL", e.target.value)
                                        }
                                        className="w-full border p-1"
                                    />
                                </td>
                                <td className="p-2 text-center">
                                    <select
                                        value={w.Type ?? ""}
                                        onChange={(e) =>
                                            handleWebhookChange("val", i, "Type", e.target.value)
                                        }
                                        className="border p-1"
                                    >
                                        <option value="discord">Discord</option>
                                        <option value="slack">Slack</option>
                                    </select>
                                </td>
                                <td className="p-2 text-center space-x-2">
                                    <button
                                        onClick={() => handleSaveNewWebhook("val", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Save
                                    </button>
                                    <button
                                        onClick={() => handleUpdateNewWebhook("val", i)}
                                        className="bg-green-500 text-white px-2 py-1 rounded text-sm"
                                    >
                                        Update
                                    </button>
                                    <button
                                        onClick={() => handleDeleteWebhook("val", w.ID)}
                                        disabled={!w.ID}
                                        className={`px-2 py-1 rounded text-sm ${!w.ID ? "bg-gray-400" : "bg-red-500 text-white"}`}
                                    >
                                        Delete
                                    </button>
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </table>
                <button
                    onClick={() => handleAddWebhook("val")}
                    className="mt-2 text-blue-600 underline"
                >
                    + Ajouter un webhook Validator
                </button>
            </section>

            {/* Alert Contacts */}
            <section>
                <h2 className="text-xl font-semibold">Contacts d'alerte</h2>
                {contacts.map((c, i) => (
                    <div key={i} className="flex gap-2 mb-2 items-center text-center">
                        <input
                            type="text"
                            placeholder="Moniker"
                            value={c.moniker}
                            onChange={(e) => handleContactChange(i, "moniker", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <input
                            type="text"
                            placeholder="Nom du contact"
                            value={c.name}
                            onChange={(e) => handleContactChange(i, "name", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <input
                            type="text"
                            placeholder="Tag de mention"
                            value={c.tag}
                            onChange={(e) => handleContactChange(i, "tag", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <div className="flex gap-1">
                            <button
                                onClick={() => handleUpdateContact(i)}
                                className="bg-green-600 text-white px-2 rounded text-center"
                            >
                                Update
                            </button>
                            <button
                                onClick={() => handleDeleteContact(i)}
                                className="bg-red-600 text-white px-2 rounded text-center"
                            >
                                Delete
                            </button>
                        </div>
                    </div>
                ))}
                <button
                    onClick={handleAddContact}
                    className="text-blue-600 underline mt-2"
                >
                    + Ajouter un contact
                </button>
            </section>
        </div>
    );
}
