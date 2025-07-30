"use client";
import { useUser } from "@clerk/nextjs";
import { useEffect, useState } from "react";

type Webhook = { ID?: number; DESCRIPTION: string; URL: string; Type: string };
type WebhookType = "gov" | "val";
type WebhookField = keyof Webhook;
type ContactAlert = { ID?: number; MONIKER: string; NAME: string; MENTIONTAG: string };

export default function ConfigBotPage() {
    const { user, isLoaded } = useUser();
    const [dailyHour, setDailyHour] = useState<number>(0);
    const [dailyMinute, setDailyMinute] = useState<number>(0);
    const [govWebhooks, setGovWebhooks] = useState<Webhook[]>([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
    const [valWebhooks, setValWebhooks] = useState<Webhook[]>([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
    const [contacts, setContacts] = useState<ContactAlert[]>([{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "" }]);

    const loadConfig = async () => {

        if (!user) return; // ✅ Secxurety
        try {
            const res = await fetch(`/api/get-webhooks?user_id=${user.id}`);
            if (!res.ok) throw new Error("Error during the loading of the config");

            const data = await res.json();
            console.log("✅ Data reçue du backend :", data);

            setGovWebhooks(data.govWebhooks?.length > 0 ? data.govWebhooks : [{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            setValWebhooks(data.valWebhooks?.length > 0 ? data.valWebhooks : [{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            setContacts(data.contacts?.length > 0 ? data.contacts : [{ ID: undefined, MONIKER: "", NAME: "", MENTIONTAG: "" }]);

            // ✅ UPdate hour if disponible 
            if (data.hour?.daily_report_hour !== undefined && data.hour?.daily_report_minute !== undefined) {
                setDailyHour(data.hour.daily_report_hour);
                setDailyMinute(data.hour.daily_report_minute);
            }
        } catch (err) {
            console.error("❌ Error during the loading of the config:", err);
        }
    };
    // Load ini of data 
    useEffect(() => {
        if (!isLoaded || !user) return;


        loadConfig();
    }, [isLoaded, user]);


    const handleWebhookChange = (type: WebhookType, index: number, field: WebhookField, value: string) => {
        const updater = type === "gov" ? setGovWebhooks : setValWebhooks;
        const current = type === "gov" ? govWebhooks : valWebhooks;

        const updated = [...current];
        updated[index] = { ...updated[index], [field]: value };
        updater(updated);
    };

    // ✅ add a wehbook (button Add)
    const handleAddWebhook = (type: WebhookType) => {
        if (type === "gov") {
            setGovWebhooks([...govWebhooks, { ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
        } else {
            setValWebhooks([...valWebhooks, { ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
        }
        return

    };

    // ✅ Save webhook info in the backend"Save"
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
                await loadConfig();

                // ✅ Mettre à jour l'ID dans le state (important pour Delete)
                if (type === "gov") {
                    const updated = [...govWebhooks];
                    updated[index] = { ...updated[index], ID: data.id }; // ✅ Force la copie                    setGovWebhooks(updated);
                } else {
                    const updated = [...valWebhooks];
                    updated[index] = { ...updated[index], ID: data.id }; // ✅ Force la copie                    setValWebhooks(updated);
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
                    id: webhook.ID,
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
        ;
    };


    async function handleDeleteWebhook(type: WebhookType, id?: number) {
        if (!id || !user?.id) {
            alert("⚠️ Impossible de supprimer : ID ou user_id manquant");
            return;
        }

        try {
            const res = await fetch(`/api/delete-webhook`, {
                method: "POST",
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

            // ✅ Recharge la liste
            await loadConfig();

            // ✅ Si liste vide, on garde une ligne par défaut
            if (type === "gov" && govWebhooks.length <= 1) {
                setGovWebhooks([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            }
            if (type === "val" && valWebhooks.length <= 1) {
                setValWebhooks([{ ID: undefined, DESCRIPTION: "", URL: "", Type: "discord" }]);
            }

            alert("✅ Webhook supprimé avec succès !");
        } catch (error) {
            console.error("❌ Erreur réseau:", error);
            alert("Erreur réseau lors de la suppression");
        }
    }
    const handleCreateContact = async (index: number) => {
        const contact = contacts[index];
        if (!contact.MONIKER || !contact.NAME || !contact.MENTIONTAG) {
            alert("⚠️ Tous les champs doivent être remplis.");
            return;
        }

        try {
            const res = await fetch("/api/add-contact-alert", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    user_id: user?.id,
                    moniker: contact.MONIKER,
                    namecontact: contact.NAME,
                    mention_tag: contact.MENTIONTAG,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API :", errorText);
                alert("Erreur lors de l’enregistrement du contact.");
            } else {
                alert("✅ Contact enregistré !");
                await loadConfig(); // Recharge les contacts avec l’ID en BDD
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau.");
        }
    };

    const handleUpdateContact = async (index: number) => {
        const contact = contacts[index];
        if (!contact.ID) {
            alert("⚠️ Ce contact n’a pas encore été enregistré.");
            return;
        }

        try {
            const res = await fetch("/api/edit-contact-alert", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    ID: contact.ID,
                    Moniker: contact.MONIKER,
                    NameContact: contact.NAME,
                    Mention_Tag: contact.MENTIONTAG,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API Update:", errorText);
                alert("Erreur lors de la mise à jour du contact.");
            } else {
                alert("✅ Contact mis à jour !");
            }
        } catch (error) {
            console.error("❌ Erreur réseau :", error);
            alert("Erreur réseau.");
        }
    };


    const handleAddContact = () => {
        setContacts([...contacts, { MONIKER: "", NAME: "", MENTIONTAG: "" }]);
    };

    const handleContactChange = (i: number, field: string, value: string) => {
        const updated = [...contacts];
        updated[i] = { ...updated[i], [field]: value }; // FIX: maintenant ça met à jour la valeur
        setContacts(updated);
    };

    async function handleDeleteContact(id?: number) {
        if (!id || !user?.id) {
            alert("⚠️ Impossible de supprimer : ID ou user_id manquant");
            return;
        }

        try {
            const res = await fetch(`/api/delete-contact-alert`, {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    id,
                }),
            });

            if (!res.ok) {
                const errorText = await res.text();
                console.error("❌ Erreur API:", errorText);
                alert("Erreur lors de la suppression");
                return;
            }

            // ✅ Recharge la liste
            await loadConfig();



            alert("✅ contact alert supprimé avec succès !");
        } catch (error) {
            console.error("❌ Erreur réseau:", error);
            alert("Erreur réseau lors de la suppression");
        }
    }
    const handleSaveHour = async () => {
        if (!user?.id) return;

        try {
            const res = await fetch("/api/update-report-hour", {
                method: "PUT",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                    hour: dailyHour,
                    minute: dailyMinute,
                    user_id: user.id,
                }),
            });

            if (!res.ok) {
                const text = await res.text();
                console.error("❌ Erreur API :", text);
                alert("Erreur lors de l'enregistrement de l'heure.");
            } else {
                alert("✅ Heure enregistrée !");
            }
        } catch (err) {
            console.error("❌ Erreur réseau :", err);
            alert("Erreur réseau.");
        }
    };


    if (!isLoaded || !user) return <p>Chargement...</p>;
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
                        onChange={(e) => setDailyHour(Number(e.target.value))}
                        className="px-2 w-16"
                    />
                    <span>:</span>
                    <input
                        type="number"
                        value={dailyMinute}
                        min={0}
                        max={59}
                        onChange={(e) => setDailyMinute(Number(e.target.value))}
                        className="px-2 w-16"
                    />
                </div>
                <button
                    onClick={handleSaveHour}
                    className="bg-blue-600 text-white px-4 py-1 rounded"
                >
                    Save
                </button>
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
                            value={c.MONIKER}
                            onChange={(e) => handleContactChange(i, "MONIKER", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <input
                            type="text"
                            placeholder="Nom du contact"
                            value={c.NAME}
                            onChange={(e) => handleContactChange(i, "NAME", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <input
                            type="text"
                            placeholder="Tag de mention"
                            value={c.MENTIONTAG}
                            onChange={(e) => handleContactChange(i, "MENTIONTAG", e.target.value)}
                            className="border p-2 w-1/4 text-center"
                        />
                        <div className="flex gap-1">
                            <button
                                onClick={() => handleCreateContact(i)}
                                className="bg-green-600 text-white px-2 rounded text-center"
                            >
                                Save
                            </button>
                            <button
                                onClick={() => handleUpdateContact(i)}
                                className="bg-green-600 text-white px-2 rounded text-center"
                            >
                                Update
                            </button>
                            <button
                                onClick={() => handleDeleteContact(c.ID)}
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
